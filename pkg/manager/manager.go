package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
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

	sessions *auth.Sessions
	limiter  *auth.Limiter
	twofaMu  sync.Mutex

	mu   sync.Mutex
	runs map[string]*run
}

type run struct {
	cfg    *ConnConfig
	client *client.Client
	cancel context.CancelFunc
	logi   *log.Instance
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

// Stop cancels every running connection.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.runs {
		r.cancel()
		delete(m.runs, id)
	}
}

// start launches the engine for one config. The connection keeps running
// across failures (reconnect loop) until stopped.
func (m *Manager) start(id string) error {
	cfg, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("config %s not found", id)
	}
	engCfg, err := cfg.toClientConfig()
	if err != nil {
		return err
	}
	m.mu.Lock()
	if _, exists := m.runs[id]; exists {
		m.mu.Unlock()
		return nil
	}
	logi := log.NewInstance("[" + cfg.Name + "]")
	cl := client.New(engCfg, "")
	cl.Log = logi
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{cfg: &cfg, client: cl, cancel: cancel, logi: logi}
	m.runs[id] = r
	m.mu.Unlock()

	go func() {
		_ = cl.Run(ctx)
		// 只有仍是当前实例时才删除登记；否则会误删重启后新实例的条目，
		// 导致旧实例永远无法被停止（重复连接、名称冲突）
		m.mu.Lock()
		if cur, ok := m.runs[id]; ok && cur == r {
			delete(m.runs, id)
		}
		m.mu.Unlock()
	}()
	return nil
}

// stop halts the engine for one config, leaving it in the store.
func (m *Manager) stop(id string) {
	m.mu.Lock()
	r, ok := m.runs[id]
	if ok {
		delete(m.runs, id)
	}
	m.mu.Unlock()
	if ok {
		r.cancel()
	}
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
