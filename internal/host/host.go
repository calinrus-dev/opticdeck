// Package host embeds the browser interface for read-only Wayland sharing.
package host

import "embed"

//go:embed index.html host.css host.js
var Files embed.FS
