// Package webbase holds the URL-prefix plumbing shared by both consoles.
// Some gateways strip the deployment prefix before forwarding and some do
// not, so the middleware accepts both forms: a request that still carries
// the prefix is trimmed, one that does not is served as-is. Outbound links
// are always written with the prefix so the browser stays inside the
// prefixed namespace.
package webbase

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Middleware wraps a console handler for deployment behind a URL prefix.
type Middleware struct {
	base string
	next http.Handler
}

// New normalizes base (leading slash, no trailing slash) and returns the
// wrapping middleware.
func New(base string, next http.Handler) *Middleware {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return &Middleware{base: base, next: next}
}

func (b *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

// Link builds a URL inside the prefixed namespace.
func (b *Middleware) Link(p string) string {
	return Link(b.base, p)
}

// Link joins an in-app path with a deployment prefix.
func Link(base, p string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" {
		return p
	}
	if p == "/" {
		return base + "/"
	}
	if strings.HasPrefix(p, "/") {
		return base + p
	}
	return base + "/" + p
}

// StripBase removes the prefix from a path that may or may not carry it.
func StripBase(base, p string) string {
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

// ReadFile reads one file from an http.FileSystem.
func ReadFile(fsys http.FileSystem, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// InjectBase inserts a <base href="base/"> element right after <head> so the
// browser resolves relative URLs against the prefixed namespace. The body is
// returned unchanged when it has no <head>.
func InjectBase(body []byte, base string) []byte {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" {
		return body
	}
	tag := []byte(`<base href="` + base + `/">`)
	if idx := bytes.Index(bytes.ToLower(body), []byte("<head>")); idx >= 0 {
		insertAt := idx + len("<head>")
		out := make([]byte, 0, len(body)+len(tag))
		out = append(out, body[:insertAt]...)
		out = append(out, tag...)
		out = append(out, body[insertAt:]...)
		return out
	}
	return append(tag, body...)
}
