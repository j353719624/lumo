package manager

import (
	"crypto/subtle"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"encoding/base64"
	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/proto"

	"github.com/j353719624/lumo/pkg/qrcode"
	"github.com/j353719624/lumo/pkg/totp"
	"github.com/j353719624/lumo/pkg/version"
	lumocweb "github.com/j353719624/lumo/web/lumoc"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = jsonEncode(w, v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

const sessionCookie = "lumo_session"

// guard enforces console authentication. With no password configured the
// console is open (local tool default).
func (m *Manager) guard(next http.Handler) http.Handler {
	return m.guardUI(next)
}

func (m *Manager) guardUI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 登录页自身与其静态资源无需认证，否则登录页的 JS/CSS 会被重定向导致白屏
		if isPublicAsset(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if m.authed(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = jsonEncode(w, map[string]string{"error": "未登录或会话已过期"})
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// isPublicAsset reports whether the path must be reachable without login.
func isPublicAsset(p string) bool {
	return p == "/login" || p == "/login.html" || p == "/help.html" || strings.HasPrefix(p, "/assets/")
}

// authed checks the session cookie, then basic auth credentials.
func (m *Manager) authed(r *http.Request) bool {
	if m.consolePass == "" {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && m.sessions.Valid(c.Value) {
		return true
	}
	u, p, ok := r.BasicAuth()
	return ok &&
		subtle.ConstantTimeCompare([]byte(u), []byte(m.consoleUser)) == 1 &&
		subtle.ConstantTimeCompare([]byte(p), []byte(m.consolePass)) == 1
}

// loginPage serves the login form when a password is configured.
func (m *Manager) loginPage(w http.ResponseWriter, r *http.Request) {
	if m.consolePass == "" || m.authed(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	b, err := fs.ReadFile(lumocweb.EmbedFS, "login.html")
	if err != nil {
		http.Error(w, "login page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// apiLogin validates console credentials and issues a session cookie.
func (m *Manager) apiLogin(w http.ResponseWriter, r *http.Request) {
	origin, _, _ := net.SplitHostPort(r.RemoteAddr)
	if m.limiter.Locked(origin) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = jsonEncode(w, map[string]string{"error": "尝试次数过多，请 10 分钟后再试"})
		return
	}
	var req struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = jsonEncode(w, map[string]string{"error": "bad request body"})
		return
	}
	okU := subtle.ConstantTimeCompare([]byte(req.Account), []byte(m.consoleUser)) == 1
	okP := subtle.ConstantTimeCompare([]byte(req.Password), []byte(m.consolePass)) == 1
	if !okU || !okP {
		m.limiter.Fail(origin)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = jsonEncode(w, map[string]string{"error": "账号或密码不正确"})
		return
	}
	m.limiter.Reset(origin)
	// 控制台 2FA 已启用：先发短期票据，凭票据 + 动态码换会话（与服务端控制台相互独立）
	if m.twoFAEnabled() {
		ticket, err := issueLoginTicket()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = jsonEncode(w, map[string]string{"error": "无法创建验证会话"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"need2fa": true, "ticket": ticket})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: m.sessions.Issue(), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// apiLogout revokes the session.
func (m *Manager) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		m.sessions.Revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// consoleFS exposes the embedded client console assets.
var consoleFS = http.FS(lumocweb.EmbedFS)

// apiTestConnection tests TCP connectivity from the NAS to a target.
func (m *Manager) apiTestConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
		Port int    `json:"port"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	if req.Addr == "" || req.Port == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "addr and port required"})
		return
	}
	addr := net.JoinHostPort(req.Addr, strconv.Itoa(req.Port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	conn.Close()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "addr": addr})
}

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
	}
	if cfg.Name != "" {
		L = append([]string{`name = "` + esc(cfg.Name) + `"`}, L...)
	}
	if cfg.Protocol == "kcp" {
		L = append(L, `protocol = "kcp"`, "kcpPort = "+strconv.Itoa(cfg.KCPPort))
	}
	if cfg.ProtocolVer > 0 && cfg.ProtocolVer != 2 {
		L = append(L, "protocolVersion = "+strconv.Itoa(cfg.ProtocolVer))
	}
	L = append(L,
		"",
		"[auth]",
		`token = "`+esc(cfg.Token)+`"`,
		"",
	)
	for _, mp := range cfg.Mappings {
		if !mp.active() {
			continue
		}
		L = append(L, "[[proxies]]",
			`name = "`+esc(cfg.ID+"-"+mp.ID)+`"`,
			`type = "`+esc(mp.Type)+`"`,
			`localIP = "`+esc(cmpOr(mp.LocalIP, "127.0.0.1"))+`"`,
			"localPort = "+strconv.Itoa(mp.LocalPort))
		if mp.Name != "" {
			L = append(L, `label = "`+esc(mp.Name)+`"`)
		}
		if mp.Bandwidth != "" {
			L = append(L, `bandwidth = "`+esc(mp.Bandwidth)+`"`)
		}
		if mp.HTTPUser != "" {
			L = append(L, `httpUser = "`+esc(mp.HTTPUser)+`"`, `httpPass = "`+esc(mp.HTTPPass)+`"`)
		}
		if mp.Compress {
			L = append(L, "compress = true")
		}
		if mp.Type == "http" || mp.Type == "https" || mp.Type == "mux" {
			ds := mp.Domains
			if len(ds) == 0 && mp.Domain != "" {
				ds = []string{mp.Domain}
			}
			quoted := make([]string, 0, len(ds))
			for _, d := range ds {
				quoted = append(quoted, `"`+esc(d)+`"`)
			}
			L = append(L, "customDomains = ["+strings.Join(quoted, ", ")+"]")
			if len(mp.SubPaths) > 0 {
				qp := make([]string, 0, len(mp.SubPaths))
				for _, p := range mp.SubPaths {
					qp = append(qp, `"`+esc(p)+`"`)
				}
				L = append(L, "subPaths = ["+strings.Join(qp, ", ")+"]")
			}
		} else if mp.Type == "sealed-tcp" || mp.Type == "sealed-udp" || mp.Type == "punch" {
			L = append(L, `sk = "`+esc(mp.SK)+`"`)
		} else {
			L = append(L, "remotePort = "+strconv.Itoa(mp.RemotePort))
		}
		L = append(L, "")
	}
	for _, v := range cfg.Visitors {
		vt := v.Type
		switch vt {
		case "sealed-tcp":
			vt = "tcp"
		case "sealed-udp":
			vt = "udp"
		case "punch":
			vt = "punch"
		}
		L = append(L, "[[visitors]]",
			`name = "`+esc(v.ID)+`"`,
			`type = "`+esc(vt)+`"`,
			`serverName = "`+esc(v.ServerName)+`"`,
			`sk = "`+esc(v.SK)+`"`,
			`bindAddr = "`+esc(cmpOr(v.BindAddr, "127.0.0.1"))+`"`,
			"bindPort = "+strconv.Itoa(v.BindPort),
			"")
	}
	return strings.Join(L, "\n")
}

// serveConsole serves embedded files; unknown paths go back to the root.
//
// HTML is always revalidated (hashed asset names make it safe to cache
// assets for a year); without this, browsers keep serving a stale index
// that references old asset hashes after an upgrade.
func serveConsole(fsys http.FileSystem) http.Handler {
	fsrv := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || strings.HasSuffix(p, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if p != "" {
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

// apiTomlSave parses an edited lumoc.toml back into the stored config,
// validates it, persists it and restarts the instance when it is running.
func (m *Manager) apiTomlSave(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cur, ok := m.store.Get(id)
	if !ok {
		fail(w, http.StatusNotFound, "config not found")
		return
	}
	var req struct {
		Toml string `json:"toml"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad request body")
		return
	}
	tmp, err := os.CreateTemp("", "lumoc-*.toml")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, _ = tmp.WriteString(req.Toml)
	_ = tmp.Close()
	parsed, err := config.LoadClient(tmpName)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(parsed.Proxies) == 0 && len(parsed.Visitors) == 0 {
		fail(w, http.StatusBadRequest, "至少保留一条端口映射或一个访问端")
		return
	}
	next := cur
	next.ServerAddr = parsed.ServerAddr
	next.ServerPort = parsed.ServerPort
	next.Protocol = parsed.Protocol
	next.KCPPort = parsed.KCPPort
	next.ProtocolVer = parsed.ProtocolVersion
	next.Token = parsed.AuthToken
	next.Mappings = nil
	next.Visitors = nil
	for _, p := range parsed.Proxies {
		mid := strings.TrimPrefix(p.Name, cur.ID+"-")
		if mid == p.Name || mid == "" {
			mid = proto.RandomHex(4)
		}
		mp := Mapping{
			ID: mid, Name: p.Label, Type: p.Type,
			LocalIP: p.LocalIP, LocalPort: p.LocalPort,
			RemotePort: p.RemotePort, SK: p.SK,
			SubPaths: p.SubPaths, Bandwidth: p.Bandwidth,
			HTTPUser: p.HTTPUser, HTTPPass: p.HTTPPass, Compress: p.Compress,
		}
		if len(p.CustomDomains) > 0 {
			mp.Domains = p.CustomDomains
			mp.Domain = p.CustomDomains[0]
		}
		next.Mappings = append(next.Mappings, mp)
	}
	for _, v := range parsed.Visitors {
		vt := v.Type
		switch vt {
		case "tcp":
			vt = "sealed-tcp"
		case "udp":
			vt = "sealed-udp"
		}
		next.Visitors = append(next.Visitors, Visitor{
			ID: proto.RandomHex(4), Name: v.Name, Type: vt,
			ServerName: v.ServerName, SK: v.SK,
			BindAddr: v.BindAddr, BindPort: v.BindPort,
		})
	}
	normalize(&next)
	if _, err := next.toClientConfig(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	_, wasRunning := m.running(id)
	if err := m.store.Upsert(&next); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	restarted := false
	if next.Enabled {
		if err := m.restart(id); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		restarted = wasRunning
	} else if wasRunning {
		m.stop(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarted": restarted, "config": next})
}

// apiLogin2FA completes a console login whose password step passed.
func (m *Manager) apiLogin2FA(w http.ResponseWriter, r *http.Request) {
	origin, _, _ := net.SplitHostPort(r.RemoteAddr)
	if m.limiter.Locked(origin) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = jsonEncode(w, map[string]string{"error": "尝试次数过多，请 10 分钟后再试"})
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !consumeLoginTicket(req.Ticket) || !m.twoFAVerify(req.Code) {
		m.limiter.Fail(origin)
		fail(w, http.StatusUnauthorized, "动态验证码不正确或已过期")
		return
	}
	m.limiter.Reset(origin)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: m.sessions.Issue(), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// api2FAStatus reports whether the console 2FA is enabled.
func (m *Manager) api2FAStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": m.twoFAEnabled()})
}

// api2FASetup mints an enrollment secret; nothing is stored until enable.
func (m *Manager) api2FASetup(w http.ResponseWriter, _ *http.Request) {
	secret, err := totp.Generate()
	if err != nil {
		fail(w, http.StatusInternalServerError, "密钥生成失败")
		return
	}
	uri := totp.URI("C", secret) // 手机令牌 App 中显示为 LUMO:C，与服务端 LUMO:S 区分
	svg := qrcode.SVG(uri, qrcode.M, 200)
	if svg == "" {
		fail(w, http.StatusInternalServerError, "二维码生成失败")
		return
	}
	svgB64 := base64.StdEncoding.EncodeToString([]byte(svg))
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret,
		"uri":    uri,
		"qr":     "data:image/svg+xml;base64," + svgB64,
	})
}

// api2FAEnable verifies a code and turns the console 2FA on.
func (m *Manager) api2FAEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !totp.Valid(req.Secret) {
		fail(w, http.StatusBadRequest, "密钥格式不正确")
		return
	}
	if !totp.Verify(req.Secret, req.Code, time.Now()) {
		fail(w, http.StatusUnauthorized, "动态验证码不正确，请输入令牌 App 当前显示的 6 位数字")
		return
	}
	m.saveTwoFA(req.Secret)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// api2FADisable turns the console 2FA off after verifying a current code.
func (m *Manager) api2FADisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !m.twoFAVerify(req.Code) {
		fail(w, http.StatusUnauthorized, "动态验证码不正确")
		return
	}
	m.saveTwoFA("")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
