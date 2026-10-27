package server

import (
	"crypto/subtle"
	"io/fs"
	"net/http"
	"strconv"

	"lumo/internal/dash"
	"lumo/internal/log"
	"lumo/internal/version"
)

// runDash serves the control console and its API using the contract
// expected by assets/api.js.
func (s *Server) runDash() {
	ui, err := fs.Sub(dash.Server, "server")
	if err != nil {
		log.Errorf("dashboard assets: %v", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/serverinfo", s.apiServerInfo)
	mux.HandleFunc("/api/proxies", s.apiProxies)
	mux.HandleFunc("/api/logs", s.apiLogs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", s.guard(http.FileServer(http.FS(ui))))

	addr := s.cfg.Web.Addr + ":" + strconv.Itoa(s.cfg.Web.Port)
	srv := &http.Server{Addr: addr, Handler: mux}
	s.dashSrv = srv
	log.Infof("dashboard listening on http://%s (user %s)", addr, s.cfg.Web.User)
	if err := srv.ListenAndServe(); err != nil && !s.closed.Load() {
		log.Errorf("dashboard: %v", err)
	}
}

// guard wraps handlers with HTTP basic authentication when enabled.
func (s *Server) guard(next http.Handler) http.Handler {
	user, pass := s.cfg.Web.User, s.cfg.Web.Password
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="LUMO"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
