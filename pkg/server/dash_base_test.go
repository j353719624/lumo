package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/webbase"
)

func newDashTestServer(base string) *Server {
	s := New(&config.ServerConfig{ConfigPath: ""})
	s.cfg.Web = config.WebServer{Addr: "127.0.0.1", Port: 0, User: "admin", Password: "pw"}
	s.SetWebBase(base)
	return s
}

func dashHandler(s *Server) http.Handler {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><head><meta charset=\"utf-8\"></head><body>ok</body></html>")},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.loginPage)
	mux.Handle("/", s.guard(serveConsole(http.FS(fsys), s.webBase)))
	return webbase.New(s.webBase, mux)
}

// 网关可能转发带前缀的路径，也可能已剥掉前缀；两种形态都必须可用，
// 且 HTML 注入了 <base> 让相对路径留在前缀命名空间里。
func TestDashBaseMiddlewareAcceptsBothPrefixedAndBarePaths(t *testing.T) {
	s := newDashTestServer("/app/lumo-server")
	h := dashHandler(s)
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:pw"))

	req := httptest.NewRequest(http.MethodGet, "/app/lumo-server/", nil)
	req.Header.Set("Authorization", auth)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("prefixed: status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<base href="/app/lumo-server/">`) {
		t.Fatalf("prefixed: <base> element not injected")
	}

	// 未认证的裸路径请求也走同一个 handler（网关剥前缀场景）
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("bare login: status %d, want 200", rec.Code)
	}
}

// 未登录访问首页必须被送回带前缀的登录页，不能跳到站点根。
func TestDashGuardRedirectKeepsPrefix(t *testing.T) {
	s := newDashTestServer("/app/lumo-server")
	h := dashHandler(s)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/lumo-server/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/app/lumo-server/login" {
		t.Fatalf("redirect %q, want /app/lumo-server/login", loc)
	}
}

// 无前缀时一切照旧：登录页 200，首页重定向到 /login。
func TestDashWithoutBaseKeepsLegacyBehaviour(t *testing.T) {
	s := newDashTestServer("")
	h := dashHandler(s)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("root redirect %d %q, want 302 /login", rec.Code, rec.Header().Get("Location"))
	}
}

// 面板数据面必须全部过认证：未登录访问一律 401（此前 serverinfo/proxies/
// logs/config 四条漏挂 guard，能匿名读取甚至改配置）。
func TestDashDataEndpointsRequireAuth(t *testing.T) {
	s := newDashTestServer("")
	h := dashHandler(s)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/serverinfo"},
		{http.MethodGet, "/api/proxies"},
		{http.MethodGet, "/api/logs"},
		{http.MethodPost, "/api/config"},
		{http.MethodGet, "/api/profile/2fa"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}
