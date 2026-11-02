package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/totp"
)

// 三层防护：Basic Auth → 面板密码 → TOTP。
// 每层都必须通过，缺任何一层都进不去。

func threeLayerServer(t *testing.T, withTOTP bool) (*Server, string) {
	t.Helper()
	s := New(&config.ServerConfig{ConfigPath: ""})
	s.cfg.Web = config.WebServer{
		Addr:          "127.0.0.1",
		Port:          0,
		User:          "panel",
		Password:      "panelpass",
		BasicUser:     "gate",
		BasicPassword: "gatepass",
	}
	if withTOTP {
		sec, err := totp.Generate()
		if err != nil {
			t.Fatal(err)
		}
		s.cfg.Web.TOTPSecrets = []string{sec}
		return s, sec
	}
	return s, ""
}

// buildHandler 组装与生产一致的三层链路。
func buildHandler(s *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.HandleFunc("POST /api/login/2fa", s.apiLogin2FA)
	mux.Handle("GET /api/serverinfo", s.guard(http.HandlerFunc(s.apiServerInfo)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return s.basicGate(mux)
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// 第一层：无 Basic 凭证一律被拒，页面 401、API 401。
func TestBasicGateBlocksWithoutCredentials(t *testing.T) {
	s, _ := threeLayerServer(t, true)
	h := buildHandler(s)

	for _, p := range []string{"/login", "/api/serverinfo"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without basic auth: %d, want 401", p, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Basic") {
			t.Fatalf("%s: missing WWW-Authenticate header", p)
		}
	}
}

// 错密码同样被拒；正确凭证才放行到下一层（此时应因未登录面板而 401/302）。
func TestBasicGateCredentialCheck(t *testing.T) {
	s, _ := threeLayerServer(t, true)
	h := buildHandler(s)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/serverinfo", nil)
	req.Header.Set("Authorization", basicHeader("gate", "wrong"))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong basic password: %d, want 401", rec.Code)
	}

	// 正确 Basic 凭证 → 进入面板层，但还没登录面板 → API 仍 401
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/serverinfo", nil)
	req.Header.Set("Authorization", basicHeader("gate", "gatepass"))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("correct basic but no panel session: %d, want 401", rec.Code)
	}
}

// 第二层：Basic 对了 + 面板密码错 → 不签发会话；面板密码对 + TOTP 未给 → 只给票据。
func TestPanelPasswordThenTOTP(t *testing.T) {
	s, sec := threeLayerServer(t, true)
	h := buildHandler(s)
	do := func(body map[string]string) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", basicHeader("gate", "gatepass"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := do(map[string]string{"account": "panel", "password": "wrong"})
	if code == http.StatusOK {
		t.Fatal("wrong panel password must not succeed")
	}

	code, out = do(map[string]string{"account": "panel", "password": "panelpass"})
	if code != http.StatusOK || out["need2fa"] != true {
		t.Fatalf("panel password ok: code=%d out=%v, want 200 + need2fa", code, out)
	}
	ticket, _ := out["ticket"].(string)
	if ticket == "" {
		t.Fatal("no ticket issued for the 2fa step")
	}

	// 错误的动态码不能换到会话（票据是一次性的，这次尝试已作废）
	code, out = do2FA(h, ticket, "000000")
	if code == http.StatusOK {
		t.Fatal("wrong TOTP code must not issue a session")
	}

	// 重新登录取一张新票据，用正确的动态码换会话
	code, out = do(map[string]string{"account": "panel", "password": "panelpass"})
	if code != http.StatusOK || out["need2fa"] != true {
		t.Fatalf("second login: code=%d out=%v", code, out)
	}
	ticket2, _ := out["ticket"].(string)
	valid, verr := totp.Code(sec, time.Now())
	if verr != nil {
		t.Fatal(verr)
	}
	code, out = do2FA(h, ticket2, valid)
	if code != http.StatusOK {
		t.Fatalf("correct TOTP: code=%d out=%v", code, out)
	}
}

// do2FA 走第二步：票据 + 动态码换会话。
func do2FA(h http.Handler, ticket, code string) (int, map[string]any) {
	b, _ := json.Marshal(map[string]string{"ticket": ticket, "code": code})
	req := httptest.NewRequest(http.MethodPost, "/api/login/2fa", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", basicHeader("gate", "gatepass"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// 未配置 basicUser 时该层关闭：面板按原样工作（会话即可进）。
func TestBasicGateDisabledWhenUnset(t *testing.T) {
	s, _ := threeLayerServer(t, true)
	s.cfg.Web.BasicUser = ""
	h := buildHandler(s)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("basic layer disabled: login page %d, want 200", rec.Code)
	}
}

// /healthz 不需要 Basic：监控探针仍可用。
func TestHealthzExemptFromBasicGate(t *testing.T) {
	s, _ := threeLayerServer(t, true)
	h := buildHandler(s)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d, want 200", rec.Code)
	}
}
