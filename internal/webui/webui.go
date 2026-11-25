// Package webui holds the built web UI. `make web` fills dist.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the UI, answering unknown paths with index.html for client-side routes.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, err := root.Open(r.URL.Path[1:]); err == nil {
			f.Close()
		} else {
			if _, err := fs.Stat(root, "index.html"); err != nil {
				http.Error(w, "the web UI is not built; run `make web`", http.StatusNotFound)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		switch r.URL.Path {
		case "/sw.js":
			// Browsers check for a new service worker only when this response is not cached.
			w.Header().Set("Cache-Control", "no-cache")
		case "/manifest.webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})
}
