package client

import (
	"context"
	"crypto/subtle"
	"io/fs"
	"net/http"
	"strconv"

	"lumo/internal/dash"
	"lumo/internal/log"
	"lumo/internal/version"
)

// httpServer is the admin dashboard server type; aliased for the Client struct.
type httpServer = http.Server

// runAdmin serves the client console until ctx is cancelled.
func (c *Client) runAdmin(ctx context.Context) {
	ui, err := fs.Sub(dash.Client, "client")
	if err != nil {
		log.Errorf("dashboard assets: %v", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", c.apiStatus)
	mux.HandleFunc("/api/reload", c.apiReload)
	mux.Handle("/", c.guard(http.FileServer(http.FS(ui))))

	addr := c.cfg.Web.Addr + ":" + strconv.Itoa(c.cfg.Web.Port)
	srv := &http.Server{Addr: addr, Handler: mux}
	c.admin = srv
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Infof("dashboard listening on http://%s (user %s)", addr, c.cfg.Web.User)
	if err := srv.ListenAndServe(); err != nil {
		log.Debugf("dashboard stopped: %v", err)
	}
}

func (c *Client) guard(next http.Handler) http.Handler {
	user, pass := c.cfg.Web.User, c.cfg.Web.Password
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

func (c *Client) apiStatus(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	list := make([]map[string]any, 0, len(c.proxies))
	for name, p := range c.proxies {
		st := c.state[name]
		entry := map[string]any{
			"name":   name,
			"type":   p.Type,
			"local":  p.LocalIP + ":" + itoa(p.LocalPort),
			"status": st.Status,
			"err":    st.Err,
		}
		if p.RemotePort > 0 {
			entry["entry"] = c.cfg.ServerAddr + ":" + itoa(p.RemotePort)
		} else if len(p.CustomDomains) > 0 {
			entry["entry"] = p.CustomDomains[0]
		}
		if cnt := c.stats[name]; cnt != nil {
			entry["traffic_in"] = cnt.In.Load()
			entry["traffic_out"] = cnt.Out.Load()
		}
		list = append(list, entry)
	}
	c.mu.Unlock()
	writeJSON(w, map[string]any{"version": version.Version, "proxies": list})
}

func (c *Client) apiReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := c.Reload(); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	log.Infof("configuration reloaded on request")
	writeJSON(w, map[string]any{"ok": true})
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
