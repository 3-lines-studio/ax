package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/botdir"
)

func TestPathIsDir(t *testing.T) {
	dir := t.TempDir()
	if !botdir.IsDir(dir) {
		t.Fatalf("expected %s to be a directory", dir)
	}
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if botdir.IsDir(file) {
		t.Fatalf("expected %s to be a file", file)
	}
	if botdir.IsDir(filepath.Join(dir, "missing")) {
		t.Fatalf("expected a missing path to not be a directory")
	}
}

func TestOpenFilesystemResolution(t *testing.T) {
	t.Run("BOT_ROOT with workspace subdir", func(t *testing.T) {
		root := t.TempDir()
		ws := filepath.Join(root, "workspace")
		if err := os.MkdirAll(ws, 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BOT_ROOT", root)
		files, err := openFilesystem()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = files.root.Close() }()
		want, _ := filepath.Abs(ws)
		if files.workspace != want {
			t.Fatalf("workspace = %q, want %q", files.workspace, want)
		}
	})
	t.Run("BOT_ROOT without workspace subdir", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("BOT_ROOT", root)
		files, err := openFilesystem()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = files.root.Close() }()
		want, _ := filepath.Abs(root)
		if files.workspace != want {
			t.Fatalf("workspace = %q, want %q", files.workspace, want)
		}
	})
	t.Run("standalone fallback with workspace", func(t *testing.T) {
		dir := t.TempDir()
		ws := filepath.Join(dir, "workspace")
		if err := os.MkdirAll(ws, 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BOT_ROOT", "")
		t.Chdir(dir)
		files, err := openFilesystem()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = files.root.Close() }()
		want, _ := filepath.Abs(ws)
		if files.workspace != want {
			t.Fatalf("workspace = %q, want %q", files.workspace, want)
		}
	})
	t.Run("standalone fallback without workspace", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("BOT_ROOT", "")
		t.Chdir(dir)
		files, err := openFilesystem()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = files.root.Close() }()
		want, _ := filepath.Abs(dir)
		if files.workspace != want {
			t.Fatalf("workspace = %q, want %q", files.workspace, want)
		}
	})
	t.Run("nonexistent BOT_ROOT errors", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "missing")
		t.Setenv("BOT_ROOT", root)
		files, err := openFilesystem()
		if err == nil {
			_ = files.root.Close()
			t.Fatal("expected openFilesystem to fail for a nonexistent root")
		}
	})
}

func TestFilesystemPath(t *testing.T) {
	files, workspace := testFilesystem(t)
	if _, err := files.path(""); err == nil {
		t.Fatal("expected an empty path to be rejected")
	}
	rel, err := files.path(filepath.Join(workspace, "a", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if rel != filepath.Join("a", "b.txt") {
		t.Fatalf("relative = %q", rel)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if _, err := files.path(outside); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected an outside-workspace error, got %v", err)
	}
}

func TestRootConfinement(t *testing.T) {
	files, workspace := testFilesystem(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "s.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := files.read(readArgs{Path: "../escape"}); err == nil {
		t.Fatal("expected a .. escape read to fail")
	}

	sub := filepath.Join(workspace, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "top.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := files.read(readArgs{Path: filepath.Join("sub", "..", "top.txt")})
	if err != nil {
		t.Fatalf("in-root .. read failed: %v", err)
	}
	if res != "hello" {
		t.Fatalf("in-root .. read = %q", res)
	}

	linkdir := filepath.Join(workspace, "ld")
	if err := os.Symlink(outside, linkdir); err != nil {
		t.Fatal(err)
	}
	if _, err := files.read(readArgs{Path: filepath.Join("ld", "s.txt")}); err == nil {
		t.Fatal("expected a symlink dir read to fail")
	}
	if _, err := files.write(writeArgs{Path: filepath.Join("ld", "s.txt"), Content: "x"}); err == nil {
		t.Fatal("expected a symlink dir write to fail")
	}
	b, err := os.ReadFile(filepath.Join(outside, "s.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "secret" {
		t.Fatalf("outside file changed: %q", b)
	}
}

func TestReadEdgeCases(t *testing.T) {
	files, workspace := testFilesystem(t)
	mk := func(name, content string) string {
		p := filepath.Join(workspace, name)
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("offset beyond end", func(t *testing.T) {
		p := mk("a.txt", "one\ntwo\n")
		off := 5
		_, err := files.read(readArgs{Path: p, Offset: &off})
		if err == nil || !strings.Contains(err.Error(), "beyond end") {
			t.Fatalf("expected beyond-end error, got %v", err)
		}
	})
	t.Run("zero offset", func(t *testing.T) {
		p := mk("b.txt", "x")
		off := 0
		_, err := files.read(readArgs{Path: p, Offset: &off})
		if err == nil || !strings.Contains(err.Error(), "offset must be positive") {
			t.Fatalf("expected positive offset error, got %v", err)
		}
	})
	t.Run("negative offset", func(t *testing.T) {
		p := mk("c.txt", "x")
		off := -1
		_, err := files.read(readArgs{Path: p, Offset: &off})
		if err == nil || !strings.Contains(err.Error(), "offset must be positive") {
			t.Fatalf("expected positive offset error, got %v", err)
		}
	})
	t.Run("negative limit", func(t *testing.T) {
		p := mk("d.txt", "x")
		limit := -1
		_, err := files.read(readArgs{Path: p, Limit: &limit})
		if err == nil || !strings.Contains(err.Error(), "limit must not be negative") {
			t.Fatalf("expected limit error, got %v", err)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		p := mk("e.txt", "")
		res, err := files.read(readArgs{Path: p})
		if err != nil {
			t.Fatal(err)
		}
		if res != "File is empty." {
			t.Fatalf("got %q", res)
		}
	})
	t.Run("whole file fits", func(t *testing.T) {
		p := mk("f.txt", "alpha\nbeta\n")
		res, err := files.read(readArgs{Path: p})
		if err != nil {
			t.Fatal(err)
		}
		if res != "alpha\nbeta" {
			t.Fatalf("got %q", res)
		}
	})
	t.Run("oversized single line", func(t *testing.T) {
		p := mk("g.txt", strings.Repeat("x", maxOutput+10))
		res, err := files.read(readArgs{Path: p})
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("[Line 1 is %d bytes, exceeds the %d limit.]", maxOutput+10, maxOutput)
		if res != want {
			t.Fatalf("got %q, want %q", res, want)
		}
	})
	t.Run("paging multi-line", func(t *testing.T) {
		line := strings.Repeat("x", 9000)
		p := mk("h.txt", line+"\n"+line+"\n"+line+"\n")
		res, err := files.read(readArgs{Path: p})
		if err != nil {
			t.Fatal(err)
		}
		want := line + fmt.Sprintf("\n\n[Showing lines 1-1 of 3 (%d byte limit). Use offset=2 to continue.]", maxOutput)
		if res != want {
			t.Fatalf("got %q, want %q", res, want)
		}
	})
}

func TestWriteBehavior(t *testing.T) {
	files, workspace := testFilesystem(t)
	path := filepath.Join(workspace, "nested", "deep", "out.txt")
	result, err := files.write(writeArgs{Path: path, Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "wrote "+path+" (5 bytes)" {
		t.Fatalf("write result = %q", result)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Fatalf("content = %q", b)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file in parent dir, got %d", len(entries))
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".fsx-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
	if _, err := files.write(writeArgs{Path: path, Content: "second"}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "second" {
		t.Fatalf("overwrite content = %q", b)
	}
}

func TestWriteEditPathAndMissingErrors(t *testing.T) {
	files, workspace := testFilesystem(t)
	outside := filepath.Join(t.TempDir(), "x.txt")
	if _, err := files.write(writeArgs{Path: outside, Content: "x"}); err == nil {
		t.Fatal("expected write with an outside path to fail")
	}
	if _, err := files.write(writeArgs{Path: "", Content: "x"}); err == nil {
		t.Fatal("expected write with an empty path to fail")
	}
	if _, err := files.edit(editArgs{Path: outside, Edits: []edit{{OldText: "a", NewText: "b"}}}); err == nil {
		t.Fatal("expected edit with an outside path to fail")
	}
	if _, err := files.edit(editArgs{Path: "", Edits: []edit{{OldText: "a", NewText: "b"}}}); err == nil {
		t.Fatal("expected edit with an empty path to fail")
	}
	missing := filepath.Join(workspace, "missing.txt")
	if _, err := files.edit(editArgs{Path: missing, Edits: []edit{{OldText: "a", NewText: "b"}}}); err == nil {
		t.Fatal("expected edit on a missing file to fail")
	}
	if _, err := files.read(readArgs{Path: missing}); err == nil {
		t.Fatal("expected read on a missing file to fail")
	}
}

func TestWriteRejectsBadParent(t *testing.T) {
	files, workspace := testFilesystem(t)
	file := filepath.Join(workspace, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := files.write(writeArgs{Path: filepath.Join(file, "sub"), Content: "x"}); err == nil {
		t.Fatal("expected write to fail when the parent path is a file")
	}
}

func TestEditEdgeCases(t *testing.T) {
	files, workspace := testFilesystem(t)
	mk := func(name, content string) string {
		p := filepath.Join(workspace, name)
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("empty edits", func(t *testing.T) {
		p := mk("a.txt", "x")
		_, err := files.edit(editArgs{Path: p})
		if err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("expected empty edits error, got %v", err)
		}
	})
	t.Run("empty oldText", func(t *testing.T) {
		p := mk("b.txt", "x")
		_, err := files.edit(editArgs{Path: p, Edits: []edit{{OldText: "", NewText: "y"}}})
		if err == nil || !strings.Contains(err.Error(), "oldText must not be empty") {
			t.Fatalf("expected empty oldText error, got %v", err)
		}
	})
	t.Run("oldText not found", func(t *testing.T) {
		p := mk("c.txt", "hello")
		_, err := files.edit(editArgs{Path: p, Edits: []edit{{OldText: "zzz", NewText: "y"}}})
		if err == nil || !strings.Contains(err.Error(), "could not find") {
			t.Fatalf("expected not-found error, got %v", err)
		}
	})
	t.Run("multiple occurrences", func(t *testing.T) {
		p := mk("d.txt", "ababab")
		_, err := files.edit(editArgs{Path: p, Edits: []edit{{OldText: "ab", NewText: "x"}}})
		if err == nil || !strings.Contains(err.Error(), "3 occurrences") {
			t.Fatalf("expected duplicate error, got %v", err)
		}
	})
	t.Run("edits applied out of order", func(t *testing.T) {
		p := mk("e.txt", "abc")
		_, err := files.edit(editArgs{Path: p, Edits: []edit{
			{OldText: "c", NewText: "C"},
			{OldText: "a", NewText: "A"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if got := read(p); got != "AbC" {
			t.Fatalf("got %q, want AbC", got)
		}
	})
	t.Run("bare CR line endings", func(t *testing.T) {
		p := mk("f.txt", "one\rtwo\r")
		_, err := files.edit(editArgs{Path: p, Edits: []edit{{OldText: "one\ntwo", NewText: "first\nsecond"}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := read(p); got != "first\nsecond\r" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestDecodeSuccess(t *testing.T) {
	var value readArgs
	decode([]byte(`{"path":"x","offset":2}`), &value)
	if value.Path != "x" || value.Offset == nil || *value.Offset != 2 || value.Limit != nil {
		t.Fatalf("decoded = %+v", value)
	}
	var w writeArgs
	decode([]byte(`{"path":"p","content":"c"}`), &w)
	if w.Path != "p" || w.Content != "c" {
		t.Fatalf("decoded writeArgs = %+v", w)
	}
}
