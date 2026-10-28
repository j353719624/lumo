//go:build !noweb

// Package lumos embeds the built lumos console assets.
package lumos

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// EmbedFS exposes the console pages with dist/ as the filesystem root.
var EmbedFS fs.FS = func() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}()
