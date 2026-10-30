package manager

import (
	"net/http"

	"github.com/j353719624/lumo/pkg/webbase"
)

// baseMiddleware makes the console work behind a URL prefix. The shared
// implementation lives in pkg/webbase; both consoles use it.
type baseMiddleware = webbase.Middleware

func newBaseMiddleware(base string, next http.Handler) http.Handler {
	return webbase.New(base, next)
}

func readFile(fsys http.FileSystem, name string) ([]byte, error) {
	return webbase.ReadFile(fsys, name)
}

func stripBase(base, p string) string {
	return webbase.StripBase(base, p)
}
