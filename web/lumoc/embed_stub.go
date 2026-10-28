//go:build noweb

package lumoc

import (
	"embed"
	"io/fs"
)

// EmbedFS is empty when console assets are excluded from the build.
var EmbedFS fs.FS = embed.FS{}
