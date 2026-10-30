package manager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A gateway may strip the /app/<name> prefix before forwarding, or it may
// forward it untouched. The console must serve both forms so the same build
// works behind either behaviour.
func TestBaseMiddlewareAcceptsBothPrefixedAndBarePaths(t *testing.T) {
	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	})
	h := newBaseMiddleware("/app/lumo", inner)

	for _, p := range []string{"/app/lumo/api/status", "/api/status"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("path %q: status %d, want 200", p, rec.Code)
		}
		if got := rec.Body.String(); got != "ok:/api/status" {
			t.Fatalf("path %q: body %q, want ok:/api/status", p, got)
		}
	}
}

// Links handed to the browser must stay inside the prefixed namespace,
// otherwise the console escapes the app frame after login.
func TestManagerLinkKeepsPrefix(t *testing.T) {
	m := &Manager{base: "/app/lumo"}
	if got := m.link("/login"); got != "/app/lumo/login" {
		t.Fatalf("link(/login) = %q", got)
	}
	if got := m.link("/"); got != "/app/lumo/" {
		t.Fatalf("link(/) = %q", got)
	}

	m.base = ""
	if got := m.link("/login"); got != "/login" {
		t.Fatalf("link without base = %q", got)
	}
}

// The HTML the console serves must carry a <base> element so relative asset
// and API references resolve under the prefix.
func TestServeConsoleInjectsBaseElement(t *testing.T) {
	fsys := consoleFS
	h := serveConsoleAt(fsys, "/app/lumo")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `<base href="/app/lumo/">`) {
		t.Fatalf("index.html has no base element: %s", body[:min(len(body), 200)])
	}
}
