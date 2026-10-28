//go:build !noweb

// Package lumoc embeds the built lumoc console assets.
package lumoc

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
