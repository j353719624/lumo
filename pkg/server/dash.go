package server

import (
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/log"
	"github.com/j353719624/lumo/pkg/version"
	lumosweb "github.com/j353719624/lumo/web/lumos"
)

const sessionCookie = "lumo_session"

// runDash serves the control console and its API using the contract
// expected by assets/api.js. Pages redirect to /login when the visitor has
// no session; API calls get 401 instead.
func (s *Server) runDash() {
	ui := lumosweb.EmbedFS
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.HandleFunc("GET /logout", s.apiLogout)
	mux.HandleFunc("/api/serverinfo", s.apiServerInfo)
	mux.HandleFunc("POST /api/config", s.apiSaveConfig)
	mux.HandleFunc("/api/proxies", s.apiProxies)
	mux.HandleFunc("/api/logs", s.apiLogs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", s.guard(serveConsole(http.FS(ui))))

	addr := s.cfg.Web.Addr + ":" + strconv.Itoa(s.cfg.Web.Port)
	srv := &http.Server{Addr: addr, Handler: mux}
	s.dashSrv = srv
	log.Infof("dashboard listening on http://%s (user %s)", addr, s.cfg.Web.User)
	if err := srv.ListenAndServe(); err != nil && !s.closed.Load() {
		log.Errorf("dashboard: %v", err)
	}
}

// guard enforces authentication: a session cookie or HTTP basic auth.
// HTML pages redirect to /login; API calls receive 401.
func (s *Server) guard(next http.Handler) http.Handler {
	user, pass := s.cfg.Web.User, s.cfg.Web.Password
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 登录页自身与其静态资源无需认证，否则登录页的 JS/CSS 会被重定向导致白屏
		if isPublicAsset(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if s.authed(r, user, pass) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("WWW-Authenticate", `Basic realm="LUMO"`)
			failJSON(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// isPublicAsset reports whether the path must be reachable without login.
func isPublicAsset(p string) bool {
	return p == "/login" || p == "/login.html" || strings.HasPrefix(p, "/assets/")
}

// authed checks the session cookie, then basic auth credentials.
func (s *Server) authed(r *http.Request, user, pass string) bool {
	if c, err := r.Cookie(sessionCookie); err == nil && s.webSessions.Valid(c.Value) {
		return true
	}
	u, p, ok := r.BasicAuth()
	return ok &&
		subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1 &&
		subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1
}

// loginPage serves the login form; already-authenticated visitors go to /.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.authed(r, s.cfg.Web.User, s.cfg.Web.Password) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	b, err := fs.ReadFile(lumosweb.EmbedFS, "login.html")
	if err != nil {
		http.Error(w, "login page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// apiLogin validates credentials, applies the failure limiter and issues a
// session cookie on success.
func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	origin, _, _ := net.SplitHostPort(r.RemoteAddr)
	if s.limiter.Locked(origin) {
		failJSON(w, http.StatusTooManyRequests, "尝试次数过多，请 10 分钟后再试")
		return
	}
	var req struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if err := jsonDecodeBody(r, &req); err != nil {
		failJSON(w, http.StatusBadRequest, "bad request body")
		return
	}
	user, pass := s.cfg.Web.User, s.cfg.Web.Password
	if subtle.ConstantTimeCompare([]byte(req.Account), []byte(user)) != 1 ||
		subtle.ConstantTimeCompare([]byte(req.Password), []byte(pass)) != 1 {
		s.limiter.Fail(origin)
		failJSON(w, http.StatusUnauthorized, "账号或密码不正确")
		return
	}
	s.limiter.Reset(origin)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.webSessions.Issue(), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	writeOK(w)
}

// apiLogout revokes the session and returns to the login page.
func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.webSessions.Revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func failJSON(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func jsonDecodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) apiServerInfo(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	typeCount := map[string]int{}
	var totalIn, totalOut uint64
	for _, ent := range s.proxies {
		typeCount[ent.kind]++
		totalIn += ent.cnt.In.Load()
		totalOut += ent.cnt.Out.Load()
	}
	clients := len(s.sessions)
	s.mu.Unlock()

	writeJSON(w, map[string]any{
		"version":          version.Version,
		"bindPort":         s.cfg.BindPort,
		"kcpPort":          0,
		"quicPort":         0,
		"vhostHttpPort":    s.cfg.VhostHTTPPort,
		"vhostHttpsPort":   s.cfg.VhostHTTPSPort,
		"subdomainHost":    "",
		"allowPortsStr":    "",
		"maxPoolCount":     0,
		"heartbeatTimeout": 90,
		"tlsForce":         false,
		"totalTrafficIn":   totalIn,
		"totalTrafficOut":  totalOut,
		"curConns":         s.connsNow.Load(),
		"clientCounts":     clients,
		"proxyTypeCount":   typeCount,
	})
}

func (s *Server) apiProxies(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	list := make([]map[string]any, 0, len(s.proxies))
	for _, ent := range s.proxies {
		conf := map[string]any{
			"type": ent.kind,
			"user": ent.owner.id,
		}
		switch ent.kind {
		case "tcp", "udp":
			conf["localIP"] = "—"
			conf["localPort"] = ent.remotePort
			conf["remotePort"] = ent.remotePort
		case "http", "https":
			conf["customDomains"] = ent.domains
		}
		list = append(list, map[string]any{
			"name":              ent.name,
			"status":            "online",
			"today_traffic_in":  ent.cnt.In.Load(),
			"today_traffic_out": ent.cnt.Out.Load(),
			"cur_conns":         0,
			"last_start_time":   ent.since.Format("2006-01-02 15:04:05"),
			"conf":              conf,
		})
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"proxies": list})
}

func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	if level == "" {
		level = "ALL"
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, map[string]any{"logs": log.Recent(level, limit)})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	writeJSONBody(w, v)
}

// serveConsole serves embedded files; unknown paths go back to the root so
// a stray link or stale bookmark never shows a bare 404.
func serveConsole(fsys http.FileSystem) http.Handler {
	fsrv := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := strings.TrimPrefix(r.URL.Path, "/"); p != "" {
			f, err := fsys.Open(strings.TrimPrefix(path.Clean("/"+p), "/"))
			if err != nil {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			_ = f.Close()
		}
		fsrv.ServeHTTP(w, r)
	})
}

// apiSaveConfig saves the updated server config and reports what needs restart.
func (s *Server) apiSaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BindPort       int    `json:"bindPort"`
		VhostHTTPPort  int    `json:"vhostHttpPort"`
		VhostHTTPSPort int    `json:"vhostHttpsPort"`
		AuthToken      string `json:"authToken"`
		WebPort        int    `json:"webPort"`
		WebPassword    string `json:"webPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad body"})
		return
	}

	oldBind := s.cfg.BindPort
	s.cfg.BindPort = req.BindPort
	s.cfg.VhostHTTPPort = req.VhostHTTPPort
	s.cfg.VhostHTTPSPort = req.VhostHTTPSPort
	s.cfg.AuthToken = req.AuthToken
	s.cfg.Web.Port = req.WebPort
	s.cfg.Web.Password = req.WebPassword

	// 写回 toml
	if s.cfgPath != "" {
		_ = config.SaveServer(s.cfgPath, s.cfg)
	}

	needsRestart := oldBind != req.BindPort
	writeJSON(w, map[string]any{"ok": true, "restart": needsRestart})
}
