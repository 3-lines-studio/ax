package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var staticFiles embed.FS

func staticRoot() (fs.FS, error) {
	return fs.Sub(staticFiles, "static")
}

func staticHandler() http.Handler {
	root, err := staticRoot()
	if err != nil {
		return nil
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		path = strings.TrimPrefix(path, "static/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(root, path); err != nil || path == "index.html" {
			r.URL.Path = "/"
		} else {
			r.URL.Path = "/" + path
		}
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasSuffix(path, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})
}
