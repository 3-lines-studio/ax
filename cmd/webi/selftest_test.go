package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/axrun"
)

// fakeSelftestAX is an ax that answers `selftest` with the given status.
func fakeSelftestAX(t *testing.T, status int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ax")
	script := `#!/bin/sh
if [ "$1" = "selftest" ]; then
  echo "ok:   ax protocol v1"
  echo "warn: no API key"
  exit ` + itoa(status) + `
fi
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

func TestSelftestPasses(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "bot.toml"), []byte(`model = "m"`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runSelftest(root, state, fakeSelftestAX(t, 0), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"ok:   bot ", "ok:   embedded UI", "ok:   state ", "ax selftest ("} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), "passed with 1 warning") {
		t.Fatalf("warning count not reported:\n%s", stdout.String())
	}
}

func TestSelftestFailsWhenAXFails(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := runSelftest(root, state, fakeSelftestAX(t, 1), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "fail: ax selftest") {
		t.Fatalf("missing failure line:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "selftest failed") {
		t.Fatalf("missing summary: %s", stderr.String())
	}
}

func TestSelftestFailsOnCorruptVault(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", t.TempDir())
	if err := os.WriteFile(filepath.Join(state, "vault.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runSelftest(root, state, fakeSelftestAX(t, 0), &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "fail: vault") {
		t.Fatalf("missing vault failure:\n%s", stdout.String())
	}
}

func TestHealthzReportsBuild(t *testing.T) {
	srv, _ := newTestServer(t, fakeSelftestAX(t, 0))
	srv.build = "abc123"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"ok":"1"`, `"build":"abc123"`, `"uptime":`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestStateReportsBuildAndRunning(t *testing.T) {
	srv, channel := newTestServer(t, fakeSelftestAX(t, 0))
	srv.build = "rev-7"
	thread, err := srv.store.CreateThread(channel, "one")
	if err != nil {
		t.Fatal(err)
	}
	// A run in the map is what the UI calls running; the count is what an
	// idle gate reads before restarting the process.
	srv.runs[thread.ID] = &axrun.Run{}
	if !srv.running(thread.ID) {
		t.Fatal("a live run must count as running")
	}
	var state struct {
		Build   string `json:"build"`
		Running int    `json:"running"`
	}
	if code := call(t, srv, "GET", "/api/state", "", &state); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if state.Build != "rev-7" {
		t.Fatalf("build = %q", state.Build)
	}
	if state.Running != 1 {
		t.Fatalf("running = %d, want 1", state.Running)
	}
}

func TestBuildID(t *testing.T) {
	t.Setenv("AX_BUILD", "")
	if got := buildID(); got != "dev" {
		t.Fatalf("buildID = %q, want dev", got)
	}
	t.Setenv("AX_BUILD", "v1-abc")
	if got := buildID(); got != "v1-abc" {
		t.Fatalf("buildID = %q, want v1-abc", got)
	}
}

func TestIsSessionBusy(t *testing.T) {
	if !isSessionBusy(errString("lock session /x: already in use")) {
		t.Fatal("busy session not recognised")
	}
	if isSessionBusy(errString("context deadline exceeded")) {
		t.Fatal("timeout must not count as busy")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
