// Package web embeds the built frontend so the binary has no external assets.
package web

import "embed"

// Dist is the Vite build output, rooted at dist/.
//
//go:embed all:dist
var Dist embed.FS
