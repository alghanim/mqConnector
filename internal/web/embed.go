// Package web exposes the embedded SvelteKit build as a filesystem the
// server can mount under "/".
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded SvelteKit build, rooted at the "dist" directory.
// If the frontend hasn't been built yet, the filesystem contains only the
// .gitkeep file and the server will respond with 404s to UI routes — this is
// intentional and not a fatal condition.
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed guarantees this can't happen — fall back to the raw FS so the
		// caller doesn't have to handle nil.
		return distFS
	}
	return sub
}
