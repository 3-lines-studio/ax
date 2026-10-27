package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runMain(t *testing.T, args []string, stdin string, env map[string]string) string {
	t.Helper()
	oldArgs, oldOut, oldErr, oldIn := os.Args, os.Stdout, os.Stderr, os.Stdin
	t.Cleanup(func() {
		os.Args, os.Stdout, os.Stderr, os.Stdin = oldArgs, oldOut, oldErr, oldIn
	})

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = out
	os.Stderr = out

	if stdin != "" {
		inPath := filepath.Join(dir, "in")
		if err := os.WriteFile(inPath, []byte(stdin), 0600); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(inPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = in.Close() }()
		os.Stdin = in
	}

	for key, value := range env {
		t.Setenv(key, value)
	}
	os.Args = append([]string{"fsx"}, args...)
	main()
	_ = out.Close()
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMainInProcess(t *testing.T) {
	t.Run("describe", func(t *testing.T) {
		out := runMain(t, []string{"describe"}, "", nil)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != len(descriptors) {
			t.Fatalf("expected %d descriptor lines, got %d", len(descriptors), len(lines))
		}
	})
	t.Run("describe dashdash", func(t *testing.T) {
		out := runMain(t, []string{"--", "describe"}, "", nil)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != len(descriptors) {
			t.Fatalf("expected %d descriptor lines, got %d", len(descriptors), len(lines))
		}
	})
	t.Run("run read", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "root")
		ws := filepath.Join(root, "workspace")
		if err := os.MkdirAll(ws, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "d.txt"), []byte("hi\n"), 0600); err != nil {
			t.Fatal(err)
		}
		out := runMain(t, []string{"run", "read"}, `{"path":"d.txt"}`, map[string]string{"BOT_ROOT": root})
		if out != "hi" {
			t.Fatalf("run read output = %q", out)
		}
	})
	t.Run("run write", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "root")
		ws := filepath.Join(root, "workspace")
		if err := os.MkdirAll(ws, 0755); err != nil {
			t.Fatal(err)
		}
		out := runMain(t, []string{"run", "write"}, `{"path":"w.txt","content":"alpha"}`, map[string]string{"BOT_ROOT": root})
		if out != "wrote w.txt (5 bytes)" {
			t.Fatalf("run write output = %q", out)
		}
		b, err := os.ReadFile(filepath.Join(ws, "w.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "alpha" {
			t.Fatalf("run write content = %q", b)
		}
	})
	t.Run("run edit", func(t *testing.T) {
		dir := t.TempDir()
		root := filepath.Join(dir, "root")
		ws := filepath.Join(root, "workspace")
		if err := os.MkdirAll(ws, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "e.txt"), []byte("one two"), 0600); err != nil {
			t.Fatal(err)
		}
		out := runMain(t, []string{"run", "edit"}, `{"path":"e.txt","edits":[{"oldText":"two","newText":"three"}]}`, map[string]string{"BOT_ROOT": root})
		if out != "edited e.txt (1 replacements)" {
			t.Fatalf("run edit output = %q", out)
		}
		b, err := os.ReadFile(filepath.Join(ws, "e.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "one three" {
			t.Fatalf("run edit content = %q", b)
		}
	})
}
