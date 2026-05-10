package ui

import "embed"

// StaticFS holds the embedded static assets (CSS, etc.).
//
//go:embed static
var StaticFS embed.FS
