// Package webui holds the built web UI. `make web` fills dist.
package webui

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var dist embed.FS

// What is worth gzipping; fonts in woff2 and images come compressed already.
var compressible = map[string]bool{".js": true, ".css": true, ".html": true, ".svg": true, ".json": true, ".webmanifest": true, ".ttf": true, ".map": true, ".txt": true}

// Handler serves the UI, answering unknown paths with index.html for client-side routes.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	return handler(root)
}

func handler(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	// Each file is compressed once, on its first request, and kept: the files never change.
	var gzipped sync.Map
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
		switch {
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			// Their names carry a hash of their content, so a name never means other bytes.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case r.URL.Path == "/", r.URL.Path == "/index.html", r.URL.Path == "/sw.js":
			// index.html names the current assets; and browsers check for a new service
			// worker only when sw.js is not cached.
			w.Header().Set("Cache-Control", "no-cache")
		case r.URL.Path == "/manifest.webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		name := r.URL.Path[1:]
		if strings.HasSuffix(r.URL.Path, "/") {
			name += "index.html"
		}
		if !compressible[path.Ext(name)] {
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) {
			files.ServeHTTP(w, r)
			return
		}
		gz, ok := gzipped.Load(name)
		if !ok {
			gz, _ = gzipped.LoadOrStore(name, compress(root, name))
		}
		if gz.([]byte) == nil {
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(gz.([]byte)))
	})
}

// compress gives a file gzipped, or nil when it cannot be read.
func compress(root fs.FS, name string) []byte {
	data, err := fs.ReadFile(root, name)
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

func acceptsGzip(r *http.Request) bool {
	for part := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		coding, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") && strings.ReplaceAll(q, " ", "") != "q=0" {
			return true
		}
	}
	return false
}
