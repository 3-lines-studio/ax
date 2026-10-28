package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestServeChildHelper is not a test: it is the child process the supervisor
// starts. It answers /healthz until SIGTERM, and fails at once when
// BOTI_CHILD_FAIL is set, which is how the crash tests work.
func TestServeChildHelper(t *testing.T) {
	addr := os.Getenv("BOTI_CHILD_ADDR")
	if addr == "" {
		t.Skip("child helper")
	}
	if os.Getenv("BOTI_CHILD_FAIL") == "1" {
		os.Exit(1)
	}
	if record := os.Getenv("BOTI_CHILD_KEY_RECORD"); record != "" {
		outcome := "none"
		if os.Getenv("AX_VAULT_FD") == "3" {
			file := os.NewFile(3, "vault-key")
			key := make([]byte, 32)
			if _, err := io.ReadFull(file, key); err != nil {
				outcome = "error: " + err.Error()
			} else {
				outcome = hex.EncodeToString(key)
			}
			_ = file.Close()
		}
		_ = os.WriteFile(record, []byte(outcome), 0o644)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":"1"}`)
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	<-signals
	_ = server.Close()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().String()
}

func testBinary(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeServeBot(t *testing.T, root, addr string) {
	t.Helper()
	content := fmt.Sprintf(`[boti.serve]
command = [%q, "-test.run=TestServeChildHelper"]
health = "http://%s/healthz"

[env]
BOTI_CHILD_ADDR = %q
`, testBinary(t), addr, addr)
	if err := os.WriteFile(filepath.Join(root, "bot.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readState(t *testing.T, path string) hostState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return hostState{}
	}
	var state hostState
	if err := json.Unmarshal(data, &state); err != nil {
		return hostState{}
	}
	return state
}

func waitState(t *testing.T, path string, timeout time.Duration, ok func(hostState) bool) hostState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if state := readState(t, path); ok(state) {
			return state
		}
		time.Sleep(50 * time.Millisecond)
	}
	state := readState(t, path)
	t.Fatalf("state never matched: %+v", state)
	return state
}

func TestServeDefaults(t *testing.T) {
	cfg := botConfig{}.serveSettings()
	if strings.Join(cfg.Command, " ") != "webi -listen "+defaultServeListen {
		t.Fatalf("command = %v", cfg.Command)
	}
	if cfg.Health != "http://"+defaultServeListen+"/healthz" {
		t.Fatalf("health = %q", cfg.Health)
	}
	custom := botConfig{Boti: botiConfig{Serve: serveConfig{Command: []string{"slaxi"}}}}.serveSettings()
	if custom.Health != "" {
		t.Fatalf("a custom command must not inherit webi's health URL: %q", custom.Health)
	}
}

func TestStatusText(t *testing.T) {
	state := hostState{PID: 10, Build: "abc", Previous: "def", Child: childState{PID: 20, Healthy: true, Started: time.Now()}, Restarts: 2, LastError: "boom"}
	text := statusText(state, true)
	for _, want := range []string{"running (pid 10", "build: abc", "previous: def", "child: pid 20, healthy", "restarts: 2", "last error: boom"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	stopped := statusText(hostState{PID: 10, Build: "abc"}, false)
	if !strings.Contains(stopped, "not running") {
		t.Fatalf("stopped status:\n%s", stopped)
	}
}

func TestTailFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tailFile(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "two\nthree\n" {
		t.Fatalf("tail = %q", got)
	}
	if _, err := tailFile(filepath.Join(t.TempDir(), "missing"), 2); err == nil {
		t.Fatal("missing log must fail")
	}
}

func TestSuperviseRestartsAfterCrash(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	addr := freeAddr(t)
	writeServeBot(t, root, addr)
	paths := newHostPaths(root)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, root) }()

	first := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Child.Healthy && s.Child.PID > 0
	})
	if first.Build != "dev" {
		t.Fatalf("build = %q, want dev without a current symlink", first.Build)
	}

	// A crash is an exit, and the supervisor starts the same build again.
	if err := syscall.Kill(first.Child.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	second := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Child.Healthy && s.Child.PID > 0 && s.Child.PID != first.Child.PID
	})
	if second.Restarts < 1 {
		t.Fatalf("restarts = %d", second.Restarts)
	}

	// The control socket restarts without waiting for a crash.
	if err := restart(root); err != nil {
		t.Fatal(err)
	}
	waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Child.Healthy && s.Child.PID > 0 && s.Child.PID != second.Child.PID
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestCrashLoopRollsBack(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	addr := freeAddr(t)
	paths := newHostPaths(root)

	// Two builds of a program named "transport"; the old one is healthy and
	// the new one dies at once.
	for _, build := range []struct{ id, body string }{
		{"a", fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestServeChildHelper\n", testBinary(t))},
		{"b", fmt.Sprintf("#!/bin/sh\nBOTI_CHILD_FAIL=1 exec %q -test.run=TestServeChildHelper\n", testBinary(t))},
	} {
		bin := filepath.Join(paths.builds, build.id, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "transport"), []byte(build.body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("b", filepath.Join(paths.builds, "current")); err != nil {
		t.Fatal(err)
	}
	state := hostState{Build: "b", Previous: "a"}
	body, _ := json.Marshal(state)
	if err := os.MkdirAll(paths.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.state, body, 0o644); err != nil {
		t.Fatal(err)
	}

	content := fmt.Sprintf(`[boti.serve]
command = ["transport"]
health = "http://%s/healthz"

[env]
BOTI_CHILD_ADDR = %q
`, addr, addr)
	if err := os.WriteFile(filepath.Join(root, "bot.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, root) }()

	final := waitState(t, paths.state, 20*time.Second, func(s hostState) bool {
		return s.Build == "a" && s.Child.Healthy
	})
	if !strings.Contains(final.LastError, "rolled back") {
		t.Fatalf("last error = %q", final.LastError)
	}
	target, err := os.Readlink(filepath.Join(paths.builds, "current"))
	if err != nil || target != "a" {
		t.Fatalf("current = %q, %v", target, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestBuildSymlink(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	h, err := newHost(root, botConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if id, bin, err := h.build(); err != nil || id != "dev" || bin != "" {
		t.Fatalf("fresh host: %q %q %v", id, bin, err)
	}
	if err := os.MkdirAll(filepath.Join(h.paths.builds, "xyz", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.switchBuild("xyz"); err != nil {
		t.Fatal(err)
	}
	if id, bin, err := h.build(); err != nil || id != "xyz" || bin == "" {
		t.Fatalf("after switch: %q %q %v", id, bin, err)
	}
	if err := h.switchBuild("../escape"); err == nil {
		t.Fatal("a build id with a path must be rejected")
	}
}

func TestBotPackagesIncludeTransport(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), `consumer = "webi"

[boti.serve]
command = ["webi", "-listen", "127.0.0.1:8377"]
`)
	pkgs, err := botPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, pkg := range pkgs {
		if pkg == "webi" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("packages = %v, want webi once", pkgs)
	}
}

func TestBotPackagesReadLocalConnections(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), `[[connection]]
id = "fs"
kind = "local"
command = "fsx"
compute = true

[[connection]]
id = "bash"
command = "bashx"

[[connection]]
id = "remote"
kind = "ssh"
host = "user@example.com"
`)
	pkgs, err := botPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, pkg := range pkgs {
		have[pkg] = true
	}
	for _, want := range []string{"ax", "slaxi", "fsx", "bashx"} {
		if !have[want] {
			t.Fatalf("missing %s in %v", want, pkgs)
		}
	}
	for _, unwanted := range []string{"remote", ""} {
		if have[unwanted] {
			t.Fatalf("unexpected %q in %v", unwanted, pkgs)
		}
	}
}

func waitFile(t *testing.T, path string, timeout time.Duration, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && ok(string(data)) {
			return string(data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	t.Fatalf("file %s = %q, %v", path, data, err)
	return ""
}

func TestServePassesVaultKey(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	addr := freeAddr(t)
	writeServeBot(t, root, addr)
	record := filepath.Join(t.TempDir(), "key")
	file, err := os.OpenFile(filepath.Join(root, "bot.toml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("BOTI_CHILD_KEY_RECORD = \"" + record + "\"\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	paths := newHostPaths(root)

	cancel, done := startServe(t, root)
	defer stopServe(t, cancel, done)
	first := waitState(t, paths.state, 10*time.Second, func(s hostState) bool { return s.Child.Healthy })
	waitFile(t, record, 5*time.Second, func(s string) bool { return s == "none" })

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	want := hex.EncodeToString(key)
	body, _ := json.Marshal(map[string]string{"key": want})
	if _, err := controlDo(paths.sock, "PUT", "/vault-key", body); err != nil {
		t.Fatal(err)
	}
	if err := restart(root); err != nil {
		t.Fatal(err)
	}
	waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Child.Healthy && s.Child.PID != first.Child.PID
	})
	waitFile(t, record, 5*time.Second, func(s string) bool { return s == want })

	if _, err := controlDo(paths.sock, "DELETE", "/vault-key", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := restart(root); err != nil {
		t.Fatal(err)
	}
	waitFile(t, record, 5*time.Second, func(s string) bool { return s == "none" })
}
