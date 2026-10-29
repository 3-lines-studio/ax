package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Unsetenv("BOT_ROOT")
	os.Unsetenv("BOT_DATA")
	os.Exit(m.Run())
}

func testFilesystem(t *testing.T) (*filesystem, string) {
	t.Helper()
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &filesystem{root: root, workspace: workspace}, workspace
}

func TestOpenFilesystemBotData(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	ws := filepath.Join(data, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_ROOT", root)
	t.Setenv("BOT_DATA", data)
	fs, err := openFilesystem()
	if err != nil {
		t.Fatalf("openFilesystem: %v", err)
	}
	defer func() { _ = fs.root.Close() }()
	if fs.workspace != ws {
		t.Errorf("workspace = %q, want %q", fs.workspace, ws)
	}
}

func TestReadFilePages(t *testing.T) {
	files, workspace := testFilesystem(t)
	path := filepath.Join(workspace, "data.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	offset := 2
	limit := 1
	result, err := files.read(readArgs{Path: path, Offset: &offset, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if result != "two\n\n[1 more lines in file. Use offset=3 to continue.]" {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestWriteAndEditFile(t *testing.T) {
	files, workspace := testFilesystem(t)
	path := filepath.Join(workspace, "nested", "data.txt")
	if _, err := files.write(writeArgs{Path: path, Content: "alpha beta gamma"}); err != nil {
		t.Fatal(err)
	}
	_, err := files.edit(editArgs{Path: path, Edits: []edit{
		{OldText: "gamma", NewText: "three"},
		{OldText: "alpha", NewText: "one"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "one beta three" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestEditPreservesCRLF(t *testing.T) {
	files, workspace := testFilesystem(t)
	path := filepath.Join(workspace, "data.txt")
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := files.edit(editArgs{Path: path, Edits: []edit{{
		OldText: "one\ntwo",
		NewText: "first\nsecond",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first\r\nsecond\r\n" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestEditRejectsDuplicateAndOverlap(t *testing.T) {
	files, workspace := testFilesystem(t)
	path := filepath.Join(workspace, "data.txt")
	if err := os.WriteFile(path, []byte("same same"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := files.edit(editArgs{Path: path, Edits: []edit{{OldText: "same", NewText: "x"}}}); err == nil {
		t.Fatal("expected duplicate error")
	}
	if err := os.WriteFile(path, []byte("abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := files.edit(editArgs{Path: path, Edits: []edit{
		{OldText: "abcd", NewText: "x"},
		{OldText: "cdef", NewText: "y"},
	}})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestFilesystemRejectsEscapes(t *testing.T) {
	files, workspace := testFilesystem(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := files.read(readArgs{Path: outside}); err == nil {
		t.Fatal("expected absolute path escape to fail")
	}
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := files.read(readArgs{Path: "link"}); err == nil {
		t.Fatal("expected symlink escape to fail")
	}
	if _, err := files.write(writeArgs{Path: "link", Content: "changed"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "secret" {
		t.Fatalf("outside file changed: %q", content)
	}
}

func TestDescriptors(t *testing.T) {
	for _, descriptor := range descriptors {
		var value map[string]any
		if err := json.Unmarshal([]byte(descriptor), &value); err != nil {
			t.Fatal(err)
		}
		if value["name"] == nil {
			t.Fatalf("descriptor has no name: %s", descriptor)
		}
	}
}
