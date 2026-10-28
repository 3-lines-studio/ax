package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getStatic(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// The UI asks for /static/..., but the embed keeps the files at the root of
// the sub-filesystem. Serving index.html for app.js is how the page ends up
// unstyled with "Unexpected token '<'".
func TestStaticServesTheShell(t *testing.T) {
	handler := staticHandler()
	if handler == nil {
		t.Fatal("no static handler")
	}
	cases := []struct {
		path string
		want string
	}{
		{"/", "<!doctype html>"},
		{"/static/app.js", "'use strict'"},
		{"/static/app.css", "/* ---"},
		{"/static/manifest.webmanifest", "{"},
		{"/thread/some-deep-link", "<!doctype html>"},
	}
	for _, tc := range cases {
		rec := getStatic(t, handler, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code %d", tc.path, rec.Code)
		}
		if !strings.HasPrefix(rec.Body.String(), tc.want) {
			t.Fatalf("%s: body starts with %q, want %q", tc.path, rec.Body.String()[:40], tc.want)
		}
	}
	if ct := getStatic(t, handler, "/static/app.js").Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("app.js content type = %q", ct)
	}
	if ct := getStatic(t, handler, "/static/app.css").Header().Get("Content-Type"); !strings.Contains(ct, "css") {
		t.Fatalf("app.css content type = %q", ct)
	}
}
