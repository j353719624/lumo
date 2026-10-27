// Package dash embeds the LUMO dashboards shipped with the binaries.
package dash

import "embed"

// Server holds the lumos control console.
//
//go:embed all:server
var Server embed.FS

// Client holds the lumoc console.
//
//go:embed all:client
var Client embed.FS
