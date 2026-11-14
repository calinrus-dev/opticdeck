// Package web contains the complete browser application and vendored noVNC.
package web

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assets embed.FS

// FS returns files relative to the web root. No files are read from disk at runtime.
func FS() fs.FS {
	root, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return root
}
