package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/log"
	"github.com/j353719624/lumo/pkg/qrcode"
	"github.com/j353719624/lumo/pkg/totp"
	"github.com/j353719624/lumo/pkg/version"
	lumosweb "github.com/j353719624/lumo/web/lumos"
)

const sessionCookie = "lumo_session"

// pending two-factor login tickets (ticket -> expiry)
var (
	twoFAMu        sync.Mutex
	twoFATickets   = map[string]time.Time{}
	twoFATicketTTL = 2 * time.Minute
)

func issue2FATicket() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(buf)
	twoFAMu.Lock()
	// opportunistic cleanup of expired tickets
	now := time.Now()
	for k, exp := range twoFATickets {
		if exp.Before(now) {
			delete(twoFATickets, k)
		}
	}
	twoFATickets[ticket] = now.Add(twoFATicketTTL)
	twoFAMu.Unlock()
	return ticket, nil
}

// consume2FATicket reports whether the ticket is valid and single-use.
func consume2FATicket(ticket string) bool {
	twoFAMu.Lock()
	defer twoFAMu.Unlock()
	exp, ok := twoFATickets[ticket]
	if !ok || exp.Before(time.Now()) {
		delete(twoFATickets, ticket)
		return false
	}
	delete(twoFATickets, ticket)
	return true
}

// runDash serves the control console and its API using the contract
// expected by assets/api.js. Pages redirect to /login when the visitor has
// no session; API calls get 401 instead.
func (s *Server) runDash() {
	ui := lumosweb.EmbedFS
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.HandleFunc("POST /api/login/2fa", s.apiLogin2FA)
	mux.HandleFunc("GET /logout", s.apiLogout)
	mux.HandleFunc("/api/profile/2fa", s.api2FAStatus)
	mux.HandleFunc("POST /api/profile/2fa/setup", s.api2FASetup)
	mux.HandleFunc("POST /api/profile/2fa/enable", s.api2FAEnable)
	mux.HandleFunc("POST /api/profile/2fa/disable", s.api2FADisable)
	mux.Handle("POST /api/test-conn", s.guard(http.HandlerFunc(s.apiTestConn)))
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
	// 二次验证已启用：先发短期票据，凭票据 + 动态码换会话
	if totp.Valid(s.cfg.Web.TOTPSecret) {
		ticket, err := issue2FATicket()
		if err != nil {
			failJSON(w, http.StatusInternalServerError, "无法创建验证会话")
			return
		}
		writeJSON(w, map[string]any{"need2fa": true, "ticket": ticket})
		return
	}
	s.limiter.Reset(origin)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.webSessions.Issue(), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	writeOK(w)
}

// apiLogin2FA completes a login whose password step passed: the short-lived
// ticket is exchanged for a session after the authenticator code verifies.
func (s *Server) apiLogin2FA(w http.ResponseWriter, r *http.Request) {
	origin, _, _ := net.SplitHostPort(r.RemoteAddr)
	if s.limiter.Locked(origin) {
		failJSON(w, http.StatusTooManyRequests, "尝试次数过多，请 10 分钟后再试")
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if err := jsonDecodeBody(r, &req); err != nil {
		failJSON(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !consume2FATicket(req.Ticket) || !totp.Verify(s.cfg.Web.TOTPSecret, req.Code, time.Now()) {
		s.limiter.Fail(origin)
		failJSON(w, http.StatusUnauthorized, "动态验证码不正确或已过期")
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

// api2FAStatus reports whether two-factor login is enabled.
func (s *Server) api2FAStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"enabled": totp.Valid(s.cfg.Web.TOTPSecret)})
}

// api2FASetup generates a fresh enrollment secret; nothing is stored until
// enable succeeds, so a half-finished bind leaves the account unchanged.
func (s *Server) api2FASetup(w http.ResponseWriter, r *http.Request) {
	secret, err := totp.Generate()
	if err != nil {
		failJSON(w, http.StatusInternalServerError, "密钥生成失败")
		return
	}
	uri := totp.URI(s.cfg.Web.User, secret)
	svg := qrcode.SVG(uri, qrcode.M, 200)
	if svg == "" {
		failJSON(w, http.StatusInternalServerError, "二维码生成失败")
		return
	}
	writeJSON(w, map[string]any{
		"secret": secret,
		"uri":    uri,
		"qr":     "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)),
	})
}

// api2FAEnable binds the secret after the user proves possession of the
// authenticator by entering a current code.
func (s *Server) api2FAEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if err := jsonDecodeBody(r, &req); err != nil {
		failJSON(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !totp.Valid(req.Secret) {
		failJSON(w, http.StatusBadRequest, "密钥格式不正确")
		return
	}
	if !totp.Verify(req.Secret, req.Code, time.Now()) {
		failJSON(w, http.StatusUnauthorized, "动态验证码不正确，请输入令牌 App 当前显示的 6 位数字")
		return
	}
	if s.cfgPath == "" {
		failJSON(w, http.StatusInternalServerError, "未找到配置文件，无法保存")
		return
	}
	s.cfg.Web.TOTPSecret = req.Secret
	if err := config.SaveServer(s.cfgPath, s.cfg); err != nil {
		s.cfg.Web.TOTPSecret = ""
		failJSON(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}
	writeOK(w)
}

// api2FADisable clears the binding after confirming a current code.
func (s *Server) api2FADisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := jsonDecodeBody(r, &req); err != nil {
		failJSON(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !totp.Valid(s.cfg.Web.TOTPSecret) {
		failJSON(w, http.StatusBadRequest, "尚未启用二次验证")
		return
	}
	if !totp.Verify(s.cfg.Web.TOTPSecret, req.Code, time.Now()) {
		failJSON(w, http.StatusUnauthorized, "动态验证码不正确")
		return
	}
	if s.cfgPath == "" {
		failJSON(w, http.StatusInternalServerError, "未找到配置文件，无法保存")
		return
	}
	s.cfg.Web.TOTPSecret = ""
	if err := config.SaveServer(s.cfgPath, s.cfg); err != nil {
		failJSON(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}
	writeOK(w)
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d天%d小时%d分", days, hours, mins)
	}
	return fmt.Sprintf("%d小时%d分", hours, mins)
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

// apiTestConn tests TCP connectivity from the server to a target.
func (s *Server) apiTestConn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad body"})
		return
	}
	addr := net.JoinHostPort(req.Addr, strconv.Itoa(req.Port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	conn.Close()
	writeJSON(w, map[string]any{"ok": true, "addr": addr})
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
		"kcpPort":          s.cfg.KCPPort,
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
		"pid":              os.Getpid(),
		"startedAt":        s.start.Format("01-02 15:04:05"),
		"uptimeStr":        formatDuration(time.Since(s.start)),
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
		case "http", "https", "mux":
			conf["customDomains"] = ent.domains
		case "sealed-tcp", "sealed-udp":
			conf["sk"] = "••••"
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
//
// HTML is always revalidated (hashed asset names make it safe to cache
// assets for a year); without this, browsers keep serving a stale index
// that references old asset hashes after an upgrade.
func serveConsole(fsys http.FileSystem) http.Handler {
	fsrv := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasSuffix(p, ".html") || p == "" || p == "/" {
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

// apiSaveConfig saves the updated server config and reports what needs restart.
func (s *Server) apiSaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BindPort       int    `json:"bindPort"`
		KCPPort        int    `json:"kcpPort"`
		VhostHTTPPort  int    `json:"vhostHttpPort"`
		VhostHTTPSPort int    `json:"vhostHttpsPort"`
		AuthToken      string `json:"authToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad body"})
		return
	}

	if req.BindPort < 1 || req.BindPort > 65535 {
		failJSON(w, http.StatusBadRequest, "bindPort 必须在 1-65535 之间")
		return
	}
	if req.KCPPort < 0 || req.KCPPort > 65535 {
		failJSON(w, http.StatusBadRequest, "kcpPort 必须在 0-65535 之间（0 表示关闭 KCP）")
		return
	}
	for name, port := range map[string]int{
		"vhostHttpPort": req.VhostHTTPPort, "vhostHttpsPort": req.VhostHTTPSPort,
	} {
		if port != 0 && (port < 1 || port > 65535) {
			failJSON(w, http.StatusBadRequest, name+" 必须在 1-65535 之间（0 表示不修改）")
			return
		}
	}
	oldVhostHTTP := s.cfg.VhostHTTPPort
	oldVhostHTTPS := s.cfg.VhostHTTPSPort
	oldBind := s.cfg.BindPort
	oldKCP := s.cfg.KCPPort
	oldToken := s.cfg.AuthToken
	if req.BindPort != oldBind {
		if err := s.Rebind(req.BindPort); err != nil {
			failJSON(w, http.StatusBadRequest, "切换监听端口失败："+err.Error())
			return
		}
		persistFirewallPort(req.BindPort)
	}
	if req.KCPPort != oldKCP {
		if err := s.StartKCP(req.KCPPort); err != nil {
			// 恢复旧监听，避免服务处于半开状态
			_ = s.StartKCP(oldKCP)
			failJSON(w, http.StatusBadRequest, "切换 KCP 端口失败："+err.Error())
			return
		}
		if req.KCPPort > 0 {
			persistFirewallPort(req.KCPPort)
		}
	}
	s.cfg.BindPort = req.BindPort
	s.cfg.KCPPort = req.KCPPort
	s.cfg.VhostHTTPPort = req.VhostHTTPPort
	s.cfg.VhostHTTPSPort = req.VhostHTTPSPort
	// 令牌由客户端生成：面板不回显、提交空值表示维持原令牌不变
	if req.AuthToken != "" {
		s.cfg.AuthToken = req.AuthToken
	}

	// 写回 toml
	if s.cfgPath != "" {
		_ = config.SaveServer(s.cfgPath, s.cfg)
	}

	needsRestart := false // 隧道端口已热切换；vhost 端口改动需要重启
	if req.VhostHTTPPort != oldVhostHTTP || req.VhostHTTPSPort != oldVhostHTTPS {
		needsRestart = true
	}
	// 任何影响连接的服务端变更：立刻断开全部客户端，强制用新配置重连
	tokenChanged := req.AuthToken != "" && req.AuthToken != oldToken
	if req.BindPort != oldBind || needsRestart || tokenChanged {
		go s.DropAllSessions("服务端配置已更新，请重新连接")
	}
	writeJSON(w, map[string]any{"ok": true, "restart": needsRestart})
}

// persistFirewallPort opens a newly-bound tunnel port in the local firewall
// layers (firewalld + the persistent nft allowlist) so the change survives
// reboots. Failures are logged, never fatal.
func persistFirewallPort(port int) {
	spec := fmt.Sprintf("%d/tcp", port)
	if err := exec.Command("firewall-cmd", "--permanent", "--add-port="+spec).Run(); err == nil {
		_ = exec.Command("firewall-cmd", "--reload").Run()
	}
	const nftPath = "/etc/egress.nft"
	data, err := os.ReadFile(nftPath)
	if err != nil {
		return
	}
	text := string(data)
	ps := strconv.Itoa(port)
	if strings.Contains(text, ps+",") || strings.Contains(text, ps+" }") {
		return
	}
	re := regexp.MustCompile(`(?m)^(\s*tcp dport \{)([0-9, ]+)(\} accept)$`)
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return
	}
	set := strings.TrimSpace(text[loc[4]:loc[5]])
	text = text[:loc[4]] + set + ", " + ps + text[loc[5]:]
	if err := os.WriteFile(nftPath, []byte(text), 0o644); err == nil {
		_ = exec.Command("systemctl", "restart", "egress-filter.service").Run()
	}
}
