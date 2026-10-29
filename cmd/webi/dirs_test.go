package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// The channel picker creates folders through POST /api/dirs and relies on a
// 404 (not a 400) to offer the "Create <path>" button for a typed path.
func TestDirsCreateAndMissing(t *testing.T) {
	srv, _ := newTestServer(t, "/bin/true")
	parent := t.TempDir()
	target := filepath.Join(parent, "one", "two")

	var out struct {
		Path string `json:"path"`
	}
	if code := call(t, srv, "POST", "/api/dirs", `{"path":"`+target+`"}`, &out); code != 200 {
		t.Fatalf("mkdir code = %d", code)
	}
	if out.Path != target {
		t.Fatalf("path = %q, want %q", out.Path, target)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}

	// Creating it again is a no-op, not an error.
	if code := call(t, srv, "POST", "/api/dirs", `{"path":"`+target+`"}`, nil); code != 200 {
		t.Fatalf("mkdir existing code = %d", code)
	}

	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := call(t, srv, "POST", "/api/dirs", `{"path":"`+file+`"}`, nil); code != 400 {
		t.Fatalf("mkdir over a file code = %d, want 400", code)
	}
	if code := call(t, srv, "POST", "/api/dirs", `{"path":"relative"}`, nil); code != 400 {
		t.Fatalf("mkdir relative code = %d, want 400", code)
	}

	missing := target + "-missing"
	if code := call(t, srv, "GET", "/api/dirs?path="+url.QueryEscape(missing), "", nil); code != 404 {
		t.Fatalf("missing dir code = %d, want 404", code)
	}
}
