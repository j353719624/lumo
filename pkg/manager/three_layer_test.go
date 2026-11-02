package manager

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/j353719624/lumo/pkg/totp"
)

// 客户端控制台的三层防护：Basic Auth → 面板密码 → TOTP。

func threeLayerManager(t *testing.T, withTOTP bool) (*Manager, string) {
	t.Helper()
	m := New(&Store{path: filepathTemp(t)})
	m.consoleUser, m.consolePass = "panel", "panelpass"
	m.SetBasicAuth("gate", "gatepass")
	if withTOTP {
		sec, err := totp.Generate()
		if err != nil {
			t.Fatal(err)
		}
		m.saveTwoFA([]string{sec})
		return m, sec
	}
	return m, ""
}

func filepathTemp(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/configs.json"
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// 组装与生产一致的三层链路。
func threeLayerHandler(m *Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", m.loginPage)
	mux.HandleFunc("POST /api/login", m.apiLogin)
	mux.HandleFunc("POST /api/login/2fa", m.apiLogin2FA)
	mux.Handle("GET /api/status", m.guard(http.HandlerFunc(m.apiStatus)))
	return m.basicGate(mux)
}

// 第一层：无 Basic 凭证一律被拒。
func TestManagerBasicGateBlocksWithoutCredentials(t *testing.T) {
	m, _ := threeLayerManager(t, true)
	h := threeLayerHandler(m)
	for _, p := range []string{"/login", "/api/status"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without basic auth: %d, want 401", p, rec.Code)
		}
	}
}

// 错密码被拒；正确凭证才进入面板层。
func TestManagerBasicGateCredentialCheck(t *testing.T) {
	m, _ := threeLayerManager(t, true)
	h := threeLayerHandler(m)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Authorization", basicHeader("gate", "wrong"))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong basic password: %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Authorization", basicHeader("gate", "gatepass"))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("correct basic but no panel session: %d, want 401", rec.Code)
	}
}

// 第二、三层：面板密码 → TOTP 才能拿到会话。
func TestManagerPanelPasswordThenTOTP(t *testing.T) {
	m, sec := threeLayerManager(t, true)
	h := threeLayerHandler(m)

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

	if code, _ := do(map[string]string{"account": "panel", "password": "wrong"}); code == http.StatusOK {
		t.Fatal("wrong panel password must not succeed")
	}
	code, out := do(map[string]string{"account": "panel", "password": "panelpass"})
	if code != http.StatusOK || out["need2fa"] != true {
		t.Fatalf("panel password ok: code=%d out=%v, want 200 + need2fa", code, out)
	}
	ticket, _ := out["ticket"].(string)
	if ticket == "" {
		t.Fatal("no ticket issued for the 2fa step")
	}

	// 错误动态码：换不到会话
	do2FA := func(tk, code string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]string{"ticket": tk, "code": code})
		req := httptest.NewRequest(http.MethodPost, "/api/login/2fa", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", basicHeader("gate", "gatepass"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var o map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &o)
		return rec.Code, o
	}
	if code, _ := do2FA(ticket, "000000"); code == http.StatusOK {
		t.Fatal("wrong TOTP code must not issue a session")
	}

	// 新票据 + 正确动态码 → 拿到会话
	code, out = do(map[string]string{"account": "panel", "password": "panelpass"})
	if code != http.StatusOK {
		t.Fatalf("second login: %d", code)
	}
	ticket2, _ := out["ticket"].(string)
	valid, verr := totp.Code(sec, time.Now())
	if verr != nil {
		t.Fatal(verr)
	}
	if code, out = do2FA(ticket2, valid); code != http.StatusOK {
		t.Fatalf("correct TOTP: code=%d out=%v", code, out)
	}
}

// 未配置 basicUser：该层关闭，控制台按原样工作。
func TestManagerBasicGateDisabledWhenUnset(t *testing.T) {
	m, _ := threeLayerManager(t, true)
	m.SetBasicAuth("", "")
	h := threeLayerHandler(m)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("basic layer disabled: login page %d, want 200", rec.Code)
	}
}
