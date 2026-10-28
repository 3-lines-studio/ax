package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The UI is pixi's: same CSS, same app.js, same PWA shell. What changed is
// where the data comes from.
//
//go:embed static
var staticFiles embed.FS

// staticRoot is the embedded UI, so a selftest can stat its files without
// building a handler.
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
		// The shell asks for /static/app.js, the convention pixi's UI uses;
		// the embedded files live at the root of the sub-filesystem, so strip
		// that one prefix before looking them up.
		//
		// The UI is a single page: unknown paths fall back to index.html so
		// deep links (a thread URL) survive a reload.
		path := strings.TrimPrefix(r.URL.Path, "/")
		path = strings.TrimPrefix(path, "static/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(root, path); err != nil || path == "index.html" {
			// FileServer redirects /index.html to /; ask for the root
			// directly so the shell is served in one request.
			r.URL.Path = "/"
		} else {
			r.URL.Path = "/" + path
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
