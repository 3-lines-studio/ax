package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests must not inherit the host bot's environment: a developer who runs the
// suite from inside a served bot has BOT_ROOT/BOT_DATA exported, and the tests
// below redirect those variables per case. Clear them once, before any test.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("BOT_ROOT")
	_ = os.Unsetenv("BOT_DATA")
	os.Exit(m.Run())
}

func TestAttachCopiesWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := t.TempDir()
	path := filepath.Join(workspace, "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_ROOT", root)
	t.Setenv("AX_ARTIFACT_DIR", artifacts)
	item, err := attach(path)
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "report.txt" || item.Size != 6 {
		t.Fatalf("artifact = %#v", item)
	}
	matches, _ := filepath.Glob(filepath.Join(artifacts, item.ID+"-*"))
	if len(matches) != 1 {
		t.Fatalf("files = %#v", matches)
	}
}

func TestAttachBotDataSplit(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	workspace := filepath.Join(data, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_ROOT", root)
	t.Setenv("BOT_DATA", data)
	t.Setenv("AX_ARTIFACT_DIR", t.TempDir())
	item, err := attach(path)
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "report.txt" || item.Size != 6 {
		t.Fatalf("artifact = %#v", item)
	}
}

func TestAttachRejectsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_ROOT", root)
	t.Setenv("AX_ARTIFACT_DIR", t.TempDir())
	if _, err := attach(outside); err == nil {
		t.Fatal("attached outside file")
	}
}
