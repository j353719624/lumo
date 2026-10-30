package manager

import (
	"io"
	"net/http"
	"strings"
)

// baseMiddleware makes the console work behind a URL prefix. Some gateways
// strip the prefix before forwarding and some do not, so the handler accepts
// both forms: a request that still carries the prefix is trimmed, one that
// does not is served as-is. Outbound links are always written with the prefix
// so the browser stays inside the prefixed namespace.
type baseMiddleware struct {
	base string
	next http.Handler
}

func newBaseMiddleware(base string, next http.Handler) http.Handler {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return &baseMiddleware{base: base, next: next}
}

func (b *baseMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if b.base != "" {
		if p := strings.TrimPrefix(r.URL.Path, b.base); p != r.URL.Path {
			r2 := r.Clone(r.Context())
			r2.URL.Path = p
			if r2.URL.Path == "" {
				r2.URL.Path = "/"
			}
			r = r2
		}
	}
	b.next.ServeHTTP(w, r)
}

// link builds a URL inside the prefixed namespace.
func (b *baseMiddleware) link(p string) string {
	if b.base == "" {
		return p
	}
	if p == "/" {
		return b.base + "/"
	}
	if strings.HasPrefix(p, "/") {
		return b.base + p
	}
	return b.base + "/" + p
}

// readFile reads one file from an http.FileSystem.
func readFile(fsys http.FileSystem, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// stripBase removes the prefix from a path that may or may not carry it.
func stripBase(base, p string) string {
	if base == "" {
		return p
	}
	if trimmed := strings.TrimPrefix(p, base); trimmed != p {
		if trimmed == "" {
			return "/"
		}
		return trimmed
	}
	return p
}
