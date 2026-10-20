// Package web holds the built page of `shade ui`: the output of `make web`, embedded in the
// binary. The Go tools build without bun, because dist/ always has its tracked .gitkeep.
package web

import (
	"embed"
	"io/fs"
)

// The all: prefix takes dot-files too. Without it .gitkeep would not count, and the pattern
// would match nothing on a checkout that was never built.
//
//go:embed all:dist
var dist embed.FS

// Dist is the built page, rooted at dist/: index.html is at its top. On a checkout that was never
// built it holds .gitkeep only.
func Dist() fs.FS {
	// fs.Sub fails only for an invalid path, and "dist" is a constant that is valid.
	sub, _ := fs.Sub(dist, "dist")
	return sub
}
