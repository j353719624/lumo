package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/j353719624/lumo/pkg/auth"
	"github.com/j353719624/lumo/pkg/client"
	"github.com/j353719624/lumo/pkg/log"
)

// Manager runs zero or more tunnel connections and serves the console.
type Manager struct {
	store *Store

	consolePort string
	consoleUser string
	consolePass string
	base        string // URL prefix the console is served under, e.g. /app/lumo
	socket      string // unix socket path; when set the console serves over it instead of TCP

	sessions *auth.Sessions
	limiter  *auth.Limiter
	twofaMu  sync.Mutex

	mu   sync.Mutex
	runs map[string]*run

	trafficMu     sync.Mutex
	traffic       map[string]*trafficAcc // 按配置 ID 的当日流量账本
	trafficFlushed time.Time             // 上次落盘时间（30s 去抖）
}

// run is one supervised engine subprocess (one per connection config).
type run struct {
	cfg      *ConnConfig
	cancel   context.CancelFunc
	logi     *log.Instance
	tomlPath string
	admin    string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool

	// 状态抓取的抖动容忍：管理口偶发超时不立刻上报「重启中」，
	// 连续 miss 超过容忍次数才降级（详见 proc.go queryStatus）。
	statusMu   sync.Mutex
	statusMiss int
	lastRun    bool
	lastErrS   string
	lastProx   []client.ProxyStatus
}

// New creates a Manager over the given store.
func New(store *Store) *Manager {
	mgr := &Manager{
		store:    store,
		runs:     map[string]*run{},
		traffic:  map[string]*trafficAcc{},
		sessions: auth.NewSessions(5 * time.Minute), // 滑动过期：有操作就续期，闲置 5 分钟退出
		limiter:  auth.NewLimiter(10 * time.Minute),
	}
	mgr.loadTwoFA()
	mgr.loadTraffic()
	return mgr
}

// trafficAcc 是一条连接配置的当日流量账本。引擎内存计数器随进程重启
// 归零，这里用「基线 + 重启检测」把它续成真正的当日累计：抓到的实时值
// 不小于上次快照就累加增量，小于则说明引擎重启过，把上次快照并入累计。
// 账本按天落盘在数据目录 traffic.json，跨应用重启同样有效。
type trafficAcc struct {
	date      string
	in, out   uint64
	lastIn    uint64
	lastOut   uint64
}

func (m *Manager) trafficPath() string {
	return filepath.Join(filepath.Dir(m.store.path), "traffic.json")
}

func (m *Manager) loadTraffic() {
	b, err := os.ReadFile(m.trafficPath())
	if err != nil {
		return
	}
	var m2 map[string]*trafficAcc
	if json.Unmarshal(b, &m2) == nil && m2 != nil {
		m.traffic = m2
	}
}

func (m *Manager) flushTrafficLocked() {
	b, err := json.Marshal(m.traffic)
	if err != nil {
		return
	}
	tmp := m.trafficPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, m.trafficPath())
	}
}

// trafficAccumulate 把引擎刚报上来的实时计数并入当日累计并返回累计值。
func (m *Manager) trafficAccumulate(id string, lin, lout uint64) (uint64, uint64) {
	m.trafficMu.Lock()
	defer m.trafficMu.Unlock()
	today := time.Now().Format("2006-01-02")
	a := m.traffic[id]
	if a == nil {
		a = &trafficAcc{}
		m.traffic[id] = a
	}
	if a.date != today {
		// 跨天：当日账本清零
		a.date, a.in, a.out, a.lastIn, a.lastOut = today, 0, 0, 0, 0
	}
	if lin >= a.lastIn {
		a.in += lin - a.lastIn
	} else {
		a.in += lin // 引擎重启过：计数器从零重新计
	}
	if lout >= a.lastOut {
		a.out += lout - a.lastOut
	} else {
		a.out += lout
	}
	a.lastIn, a.lastOut = lin, lout
	if now := time.Now(); now.Sub(m.trafficFlushed) >= 30*time.Second {
		m.trafficFlushed = now
		m.flushTrafficLocked()
	}
	return a.in, a.out
}

// SetBase sets the URL prefix the console is served under.
func (m *Manager) SetBase(base string) {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	m.base = base
}

// SetSocket makes the console listen on a unix socket instead of TCP.
// The fnOS app gateway forwards /app/<name>/* to that socket.
func (m *Manager) SetSocket(path string) {
	m.socket = strings.TrimSpace(path)
}

// Run starts every config marked enabled, serves the console and blocks
// until the process is interrupted.
func (m *Manager) Run(addr, user, pass string) error {
	m.consoleUser, m.consolePass = user, pass
	for _, cfg := range m.store.List() {
		if cfg.Enabled {
			if err := m.start(cfg.ID); err != nil {
				log.Warnf("autostart %q: %v", cfg.Name, err)
			}
		}
	}
	// SIGTERM 必须一并监听：应用管理器（systemd、fnOS 应用中心）停服务时发的是
	// SIGTERM，只监听 os.Interrupt 会让进程被默认动作直接杀掉，engine 子进程
	// 变成孤儿继续占用连接与端口。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, port, err := net.SplitHostPort(addr); err == nil {
		m.consolePort = port
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", m.loginPage)
	mux.Handle("POST /api/login", http.HandlerFunc(m.apiLogin))
	mux.Handle("GET /logout", http.HandlerFunc(m.apiLogout))
	// 桌面窗口/浏览器标签关闭时前端 sendBeacon 调用（与服务端控制台一致）：
	// 会话进入 10s 宽限，期间有请求即恢复（刷新页面无感），无请求则到点失效
	mux.HandleFunc("POST /api/session/detach", m.apiSessionDetach)
	// 换会话的端点必须免 guard：调用者此刻只有短期票据，还没有会话；
	// 票据本身一次性且 2 分钟过期，外加登录限流兜底
	mux.Handle("POST /api/login/2fa", http.HandlerFunc(m.apiLogin2FA))
	mux.Handle("GET /api/profile/2fa", m.guard(http.HandlerFunc(m.api2FAStatus)))
	mux.Handle("POST /api/profile/2fa/setup", m.guard(http.HandlerFunc(m.api2FASetup)))
	mux.Handle("POST /api/profile/2fa/enable", m.guard(http.HandlerFunc(m.api2FAEnable)))
	mux.Handle("POST /api/profile/2fa/disable", m.guard(http.HandlerFunc(m.api2FADisable)))
	mux.Handle("GET /api/configs", m.guard(http.HandlerFunc(m.apiList)))
	mux.Handle("POST /api/configs", m.guard(http.HandlerFunc(m.apiCreate)))
	mux.Handle("GET /api/configs/{id}", m.guard(http.HandlerFunc(m.apiGet)))
	mux.Handle("PUT /api/configs/{id}", m.guard(http.HandlerFunc(m.apiUpdate)))
	mux.Handle("DELETE /api/configs/{id}", m.guard(http.HandlerFunc(m.apiDelete)))
	mux.Handle("POST /api/configs/{id}/start", m.guard(http.HandlerFunc(m.apiStart)))
	mux.Handle("POST /api/configs/{id}/stop", m.guard(http.HandlerFunc(m.apiStop)))
	mux.Handle("GET /api/configs/{id}/toml", m.guard(http.HandlerFunc(m.apiToml)))
	mux.Handle("POST /api/configs/{id}/toml", m.guard(http.HandlerFunc(m.apiTomlSave)))
	mux.Handle("GET /api/status", m.guard(http.HandlerFunc(m.apiStatus)))
	mux.Handle("GET /api/info", m.guard(http.HandlerFunc(m.apiInfo)))
	mux.Handle("/", m.guardUI(serveConsoleAt(consoleFS, m.base)))

	var handler http.Handler = mux
	if m.base != "" {
		handler = newBaseMiddleware(m.base, mux)
	}

	srv := &http.Server{Addr: addr, Handler: handler}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		m.Stop()
	}()

	// unix socket 与 TCP 同时提供：socket 给应用网关（桌面入口），
	// TCP 给局域网直连与排障。两者共用同一个 handler。
	started := false
	if m.socket != "" {
		_ = os.Remove(m.socket)
		if err := os.MkdirAll(filepath.Dir(m.socket), 0o755); err != nil {
			return err
		}
		ln, err := net.Listen("unix", m.socket)
		if err != nil {
			return err
		}
		_ = os.Chmod(m.socket, 0o660)
		log.Infof("lumoc console listening on unix://%s", m.socket)
		started = true
		go func() {
			if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
				log.Errorf("console socket: %v", err)
			}
		}()
	}
	if addr != "" {
		log.Infof("lumoc console listening on http://%s", addr)
		started = true
		go func() {
			if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
				log.Errorf("console tcp: %v", err)
			}
		}()
	}
	if !started {
		return fmt.Errorf("console: neither --addr nor --socket given")
	}
	<-ctx.Done()
	return nil
}

// Stop terminates every engine subprocess.
func (m *Manager) Stop() {
	m.mu.Lock()
	runs := make([]*run, 0, len(m.runs))
	for id, r := range m.runs {
		runs = append(runs, r)
		delete(m.runs, id)
	}
	m.mu.Unlock()
	for _, r := range runs {
		r.cancel()
		r.terminate()
		_ = os.Remove(r.tomlPath)
	}
	m.trafficMu.Lock()
	m.flushTrafficLocked()
	m.trafficMu.Unlock()
}

// start launches the engine subprocess for one config. The supervisor keeps
// it running across crashes until explicitly stopped.
func (m *Manager) start(id string) error {
	cfg, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("config %s not found", id)
	}
	m.mu.Lock()
	if _, exists := m.runs[id]; exists {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// 渲染独立配置文件；引擎进程从这里读取自己的连接参数
	tomlPath := filepath.Join(filepath.Dir(m.store.path), "engine-"+id+".toml")
	if err := os.WriteFile(tomlPath, []byte(renderToml(cfg)), 0600); err != nil {
		return err
	}
	// 本地状态端口：探测空闲端口后让给引擎
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.Remove(tomlPath)
		return err
	}
	admin := ln.Addr().String()
	_ = ln.Close()

	logi := log.NewInstance("[" + cfg.Name + "]")
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{cfg: &cfg, cancel: cancel, logi: logi, tomlPath: tomlPath, admin: admin}
	m.mu.Lock()
	m.runs[id] = r
	m.mu.Unlock()

	r.logi.Infof("引擎进程启动（独立进程，admin %s）", admin)
	go r.supervise(ctx)
	return nil
}

// stop halts the engine subprocess for one config, leaving it in the store.
func (m *Manager) stop(id string) {
	m.mu.Lock()
	r, ok := m.runs[id]
	if ok {
		delete(m.runs, id)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.cancel()
	r.terminate()
	_ = os.Remove(r.tomlPath)
}

// restart applies a new config to a running connection.
func (m *Manager) restart(id string) error {
	m.stop(id)
	cfg, ok := m.store.Get(id)
	if !ok || !cfg.Enabled {
		return nil
	}
	return m.start(id)
}

func (m *Manager) running(id string) (*run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	return r, ok
}
