package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/j353719624/lumo/pkg/config"
)

// 走真实 HTTP 链路验证：detach → 宽限期内可用（活动重挂载）→ 宽限期外 401。
func TestDashSessionDetachOverHTTP(t *testing.T) {
	s := New(&config.ServerConfig{ConfigPath: ""})
	s.cfg.Web = config.WebServer{Addr: "127.0.0.1", Port: 0, User: "admin", Password: "pw"}
	// 缩短 TTL 不影响本用例；detach 宽限 10s 是编译期常量，这里直接操控内部状态
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session/detach", s.apiSessionDetach)
	mux.Handle("/api/serverinfo", s.guard(http.HandlerFunc(s.apiServerInfo)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 手工发一个会话 cookie（登录路径已被其他用例覆盖）
	tok := s.webSessions.Issue()
	cookie := &http.Cookie{Name: "lumo_session", Value: tok}

	post := func(path string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+path, nil)
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	get := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if resp := post("/api/session/detach"); resp.StatusCode != 200 {
		t.Fatalf("detach: %d", resp.StatusCode)
	}
	// 宽限期内 GET：活动重挂载
	time.Sleep(1500 * time.Millisecond)
	if resp := get("/api/serverinfo"); resp.StatusCode != 200 {
		t.Fatalf("within grace: %d", resp.StatusCode)
	}
	// 再次 detach，不再活动，11s 后必须 401
	if resp := post("/api/session/detach"); resp.StatusCode != 200 {
		t.Fatalf("detach#2: %d", resp.StatusCode)
	}
	if s.webSessions.DetachedAt(tok).IsZero() {
		t.Fatal("session should be detached after the beacon")
	}
	time.Sleep(11 * time.Second)
	resp := get("/api/serverinfo")
	if resp.StatusCode != 401 {
		var body json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&body)
		t.Fatalf("past grace: %d %s, want 401", resp.StatusCode, body)
	}
}
