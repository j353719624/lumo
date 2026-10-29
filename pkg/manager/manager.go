package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/j353719624/lumo/pkg/auth"
	"github.com/j353719624/lumo/pkg/log"
)

// Manager runs zero or more tunnel connections and serves the console.
type Manager struct {
	store *Store

	consolePort string
	consoleUser string
	consolePass string

	sessions *auth.Sessions
	limiter  *auth.Limiter
	twofaMu  sync.Mutex

	mu   sync.Mutex
	runs map[string]*run
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
}

// New creates a Manager over the given store.
func New(store *Store) *Manager {
	mgr := &Manager{
		store:    store,
		runs:     map[string]*run{},
		sessions: auth.NewSessions(7 * 24 * time.Hour),
		limiter:  auth.NewLimiter(10 * time.Minute),
	}
	mgr.loadTwoFA()
	return mgr
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer stop()

	if _, port, err := net.SplitHostPort(addr); err == nil {
		m.consolePort = port
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", m.loginPage)
	mux.Handle("POST /api/login", http.HandlerFunc(m.apiLogin))
	mux.Handle("GET /logout", http.HandlerFunc(m.apiLogout))
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
	mux.Handle("/", m.guardUI(serveConsole(consoleFS)))

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		m.Stop()
	}()
	log.Infof("lumoc console listening on http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
		return err
	}
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

