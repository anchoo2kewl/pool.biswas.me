// Package web embeds the built web application so the binary ships alone.
package web

import "embed"

// Files holds the static application: HTML, CSS, and JavaScript.
//
//go:embed static
var Files embed.FS
