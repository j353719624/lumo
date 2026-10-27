package manager

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"

	"lumo/internal/dash"
	"lumo/internal/proto"
	"lumo/internal/version"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = jsonEncode(w, v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// guard enforces basic auth when a password is configured.
func (m *Manager) guard(user, pass string, next http.HandlerFunc) http.Handler {
	return m.guardUI(user, pass, next)
}

func (m *Manager) guardUI(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pass != "" {
			u, p, ok := r.BasicAuth()
			if !ok ||
				subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
				subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="LUMO"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// consoleFS exposes the embedded client console assets.
var consoleFS = func() http.FileSystem {
	sub, err := subFS(dash.Client, "client")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}()

// normalize fills generated fields and applies defaults to a submitted config.
func normalize(c *ConnConfig) {
	if c.ID == "" {
		c.ID = proto.RandomHex(4)
	}
	if c.Name == "" {
		c.Name = "未命名配置"
	}
	if c.ServerPort == 0 {
		c.ServerPort = 7000
	}
	if c.Protocol == "" {
		c.Protocol = "tcp"
	}
	if c.Mappings == nil {
		c.Mappings = []Mapping{}
	}
	for i := range c.Mappings {
		if c.Mappings[i].ID == "" {
			c.Mappings[i].ID = proto.RandomHex(4)
		}
		if c.Mappings[i].Type == "" {
			c.Mappings[i].Type = "tcp"
		}
	}
}

func (m *Manager) apiList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, m.store.List())
}

func (m *Manager) apiGet(w http.ResponseWriter, r *http.Request) {
	cfg, ok := m.store.Get(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "config not found")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (m *Manager) apiCreate(w http.ResponseWriter, r *http.Request) {
	cfg := ConnConfig{}
	if err := jsonDecode(r.Body, &cfg); err != nil {
		fail(w, http.StatusBadRequest, "bad request body")
		return
	}
	startNow := cfg.Enabled // the UI sends startNow via Enabled
	normalize(&cfg)
	cfg.Enabled = false
	if _, err := cfg.toClientConfig(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := m.store.Upsert(&cfg); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if startNow {
		cfg.Enabled = true
		_ = m.store.Upsert(&cfg)
		if err := m.start(cfg.ID); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (m *Manager) apiUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	old, ok := m.store.Get(id)
	if !ok {
		fail(w, http.StatusNotFound, "config not found")
		return
	}
	cfg := old
	if err := jsonDecode(r.Body, &cfg); err != nil {
		fail(w, http.StatusBadRequest, "bad request body")
		return
	}
	cfg.ID = id
	normalize(&cfg)
	if _, err := cfg.toClientConfig(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	_, wasRunning := m.running(id)
	if err := m.store.Upsert(&cfg); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	restarted := false
	if cfg.Enabled {
		if err := m.restart(id); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		restarted = wasRunning
	} else if wasRunning {
		m.stop(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarted": restarted, "config": cfg})
}

func (m *Manager) apiDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m.stop(id)
	if err := m.store.Delete(id); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) apiStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, ok := m.store.Get(id)
	if !ok {
		fail(w, http.StatusNotFound, "config not found")
		return
	}
	if !cfg.Enabled {
		cfg.Enabled = true
		if err := m.store.Upsert(&cfg); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := m.start(id); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (m *Manager) apiStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, ok := m.store.Get(id)
	if !ok {
		fail(w, http.StatusNotFound, "config not found")
		return
	}
	if cfg.Enabled {
		cfg.Enabled = false
		if err := m.store.Upsert(&cfg); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	m.stop(id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) apiToml(w http.ResponseWriter, r *http.Request) {
	cfg, ok := m.store.Get(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "config not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"toml": renderToml(cfg)})
}

func (m *Manager) apiStatus(w http.ResponseWriter, _ *http.Request) {
	out := make([]map[string]any, 0, len(m.store.List()))
	for _, cfg := range m.store.List() {
		entry := map[string]any{"id": cfg.ID, "running": false, "proxies": []any{}, "log": []string{}}
		if r, ok := m.running(cfg.ID); ok {
			running, lastErr, proxies := r.client.State()
			entry["running"] = running
			if lastErr != "" {
				entry["error"] = lastErr
			}
			entry["proxies"] = proxies
			lines := make([]string, 0, 40)
			for _, e := range r.logi.Recent(40) {
				lines = append(lines, e.Time.Format("15:04:05")+" ["+e.Level+"] "+e.Msg)
			}
			entry["log"] = lines
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Manager) apiInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":   version.Version,
		"engineBin": "内置引擎（单进程托管全部连接）",
		"dataDir":   m.store.path,
		"port":      m.consolePort,
	})
}

// renderToml produces the lumoc.toml equivalent of a stored config.
func renderToml(cfg ConnConfig) string {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
	L := []string{
		`serverAddr = "` + esc(cfg.ServerAddr) + `"`,
		"serverPort = " + strconv.Itoa(cfg.ServerPort),
		"",
		"[auth]",
		`token = "` + esc(cfg.Token) + `"`,
		"",
	}
	for _, mp := range cfg.Mappings {
		if !mp.active() {
			continue
		}
		L = append(L, "[[proxies]]",
			`name = "`+esc(cfg.ID+"-"+mp.ID)+`"`,
			`type = "`+esc(mp.Type)+`"`,
			`localIP = "`+esc(cmpOr(mp.LocalIP, "127.0.0.1"))+`"`,
			"localPort = "+strconv.Itoa(mp.LocalPort))
		if mp.Type == "http" || mp.Type == "https" {
			L = append(L, `customDomains = ["`+esc(mp.Domain)+`"]`)
		} else {
			L = append(L, "remotePort = "+strconv.Itoa(mp.RemotePort))
		}
		L = append(L, "")
	}
	return strings.Join(L, "\n")
}
