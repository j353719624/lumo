package manager

import (
	"encoding/json"
	"io"
	"io/fs"
)

func jsonEncode(w io.Writer, v any) error      { return json.NewEncoder(w).Encode(v) }
func jsonDecode(r io.Reader, v any) error      { return json.NewDecoder(r).Decode(v) }
func subFS(f fs.FS, dir string) (fs.FS, error) { return fs.Sub(f, dir) }
