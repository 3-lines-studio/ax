package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func runCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// fakeRepo is a checkout package.sh can "build" without a compiler: one
// executable per program, and an ax that passes its selftest.
func fakeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, dir := range []string{"scripts", "cmd/ax", "cmd/slaxi"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	packageScript := `#!/bin/sh
set -eu
dest=
while [ $# -gt 0 ]; do
  case "$1" in
    --dest) dest=$2; shift 2 ;;
    *) break ;;
  esac
done
for program in "$@"; do
  cat > "$dest/$program" <<'BIN'
#!/bin/sh
case "$1" in
  selftest) echo "ok:   ax protocol v1"; exit 0 ;;
esac
exit 0
BIN
  chmod 0755 "$dest/$program"
done
`
	if err := os.WriteFile(filepath.Join(repo, "scripts", "package.sh"), []byte(packageScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repo, "git", "init", "-q")
	runCommand(t, repo, "git", "config", "user.email", "t@example.com")
	runCommand(t, repo, "git", "config", "user.name", "t")
	runCommand(t, repo, "git", "add", ".")
	runCommand(t, repo, "git", "commit", "-qm", "one")
	return repo
}

func fakeGo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "go")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writePromoteBot(t *testing.T, root, addr, repo string) {
	t.Helper()
	content := fmt.Sprintf(`[boti.serve]
command = [%q, "-test.run=TestServeChildHelper"]
health = "http://%s/healthz"
repo = %q

[env]
BOTI_CHILD_ADDR = %q
`, testBinary(t), addr, repo, addr)
	if err := os.WriteFile(filepath.Join(root, "bot.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func startServe(t *testing.T, root string) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, root) }()
	return cancel, done
}

func stopServe(t *testing.T, cancel context.CancelFunc, done chan error) {
	t.Helper()
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

func TestPromoteBuildsAndActivates(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	t.Setenv("PATH", fakeGo(t)+":/usr/bin:/bin")
	repo := fakeRepo(t)
	addr := freeAddr(t)
	writePromoteBot(t, root, addr, repo)
	paths := newHostPaths(root)

	cancel, done := startServe(t, root)
	defer stopServe(t, cancel, done)
	waitState(t, paths.state, 10*time.Second, func(s hostState) bool { return s.Child.Healthy })

	if err := promote(root, promoteOptions{}); err != nil {
		t.Fatal(err)
	}

	rev := strings.TrimSpace(mustRun(t, repo, "git", "rev-parse", "--short=7", "HEAD"))
	state := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Build == rev && s.Child.Healthy
	})
	if state.Previous != "" {
		t.Fatalf("previous = %q, want empty when coming from dev", state.Previous)
	}
	for _, program := range []string{"ax", "slaxi"} {
		path := filepath.Join(paths.builds, rev, "bin", program)
		if info, err := os.Stat(path); err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("%s: %v", path, err)
		}
	}
	target, err := os.Readlink(filepath.Join(paths.builds, "current"))
	if err != nil || target != rev {
		t.Fatalf("current = %q, %v", target, err)
	}
	meta := readMeta(filepath.Join(paths.builds, rev, "meta.json"))
	if meta.Rev != rev || meta.Dirty {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestPromoteDirtyCheckout(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	t.Setenv("PATH", fakeGo(t)+":/usr/bin:/bin")
	repo := fakeRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	writePromoteBot(t, root, addr, repo)
	paths := newHostPaths(root)

	cancel, done := startServe(t, root)
	defer stopServe(t, cancel, done)
	waitState(t, paths.state, 10*time.Second, func(s hostState) bool { return s.Child.Healthy })

	if err := promote(root, promoteOptions{}); err != nil {
		t.Fatal(err)
	}
	rev := strings.TrimSpace(mustRun(t, repo, "git", "rev-parse", "--short=7", "HEAD"))
	state := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return strings.HasPrefix(s.Build, rev+"-dirty") && s.Child.Healthy
	})
	if state.Build != rev+"-dirty" {
		t.Fatalf("build = %q", state.Build)
	}
	if meta := readMeta(filepath.Join(paths.builds, state.Build, "meta.json")); !meta.Dirty {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestPromoteNeedsServe(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	repo := fakeRepo(t)
	writePromoteBot(t, root, freeAddr(t), repo)
	err := promote(root, promoteOptions{})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v", err)
	}
}

func TestActivateAndRollback(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	addr := freeAddr(t)
	writeServeBot(t, root, addr)
	paths := newHostPaths(root)
	for _, id := range []string{"a", "b"} {
		bin := filepath.Join(paths.builds, id, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestServeChildHelper\n", testBinary(t))
		if err := os.WriteFile(filepath.Join(bin, "transport"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a", filepath.Join(paths.builds, "current")); err != nil {
		t.Fatal(err)
	}
	state := hostState{Build: "a"}
	body, _ := json.Marshal(state)
	if err := os.MkdirAll(paths.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.state, body, 0o644); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServe(t, root)
	defer stopServe(t, cancel, done)
	first := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Build == "a" && s.Child.Healthy
	})

	reply, err := activate(paths.sock, "b", "now")
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Activated || reply.Pending {
		t.Fatalf("reply = %+v", reply)
	}
	second := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Build == "b" && s.Child.Healthy && s.Child.PID != first.Child.PID
	})
	if second.Previous != "a" {
		t.Fatalf("previous = %q", second.Previous)
	}

	if err := rollback(root); err != nil {
		t.Fatal(err)
	}
	third := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Build == "a" && s.Child.Healthy && s.Child.PID != second.Child.PID
	})
	if third.Previous != "b" {
		t.Fatalf("previous = %q", third.Previous)
	}
}

func TestActivateWaitsForIdle(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	var running atomic.Int64
	running.Store(1)
	idleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"running": running.Load()})
	}))
	defer idleServer.Close()

	addr := freeAddr(t)
	writeServeBot(t, root, addr)
	// Add the idle URL to the bot after writeServeBot wrote the table.
	path := filepath.Join(root, "bot.toml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "[env]", "idle = \""+idleServer.URL+"\"\n\n[env]", 1))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	paths := newHostPaths(root)
	for _, id := range []string{"a", "b"} {
		bin := filepath.Join(paths.builds, id, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestServeChildHelper\n", testBinary(t))
		if err := os.WriteFile(filepath.Join(bin, "transport"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a", filepath.Join(paths.builds, "current")); err != nil {
		t.Fatal(err)
	}

	cancel, done := startServe(t, root)
	defer stopServe(t, cancel, done)
	waitState(t, paths.state, 10*time.Second, func(s hostState) bool { return s.Child.Healthy && s.Build == "a" })

	reply, err := activate(paths.sock, "b", "idle")
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Pending || reply.Activated {
		t.Fatalf("reply = %+v", reply)
	}
	pending := waitState(t, paths.state, 5*time.Second, func(s hostState) bool { return s.Pending == "b" })
	if pending.Build != "a" {
		t.Fatalf("activated while busy: %+v", pending)
	}
	time.Sleep(1500 * time.Millisecond)
	if state := readState(t, paths.state); state.Build != "a" {
		t.Fatalf("switched while running: %+v", state)
	}

	running.Store(0)
	final := waitState(t, paths.state, 10*time.Second, func(s hostState) bool {
		return s.Build == "b" && s.Child.Healthy && s.Pending == ""
	})
	if final.Previous != "a" {
		t.Fatalf("previous = %q", final.Previous)
	}
}

func TestPlaceFileLinksIdentical(t *testing.T) {
	stage, prev, out := t.TempDir(), t.TempDir(), t.TempDir()
	src := filepath.Join(stage, "ax")
	if err := os.WriteFile(src, []byte("same bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	donor := filepath.Join(prev, "ax")
	if err := os.WriteFile(donor, []byte("same bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(out, "ax")
	if err := placeFile(src, dst, []string{prev}); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(donor)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Fatal("identical binaries were copied, not linked")
	}
}

func TestUniqueBuild(t *testing.T) {
	builds := t.TempDir()
	if got := uniqueBuild(builds, "abc"); got != "abc" {
		t.Fatalf("first = %q", got)
	}
	if err := os.MkdirAll(filepath.Join(builds, "abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := uniqueBuild(builds, "abc"); got != "abc-2" {
		t.Fatalf("second = %q", got)
	}
}

func TestPruneBuildsKeepsCurrentAndPrevious(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	t.Setenv("BOT_DATA", data)
	h, err := newHost(root, botConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	for _, id := range []string{"a", "b", "c", "d"} {
		dir := filepath.Join(h.paths.builds, id)
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeMeta(filepath.Join(dir, "meta.json"), buildMeta{ID: id, Created: time.Now().Add(time.Duration(len(id)) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	h.state.Build, h.state.Previous = "a", "b"
	h.pruneBuilds(3)
	for _, id := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(h.paths.builds, id)); err != nil {
			t.Fatalf("%s was pruned: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(h.paths.builds, "d")); err != nil {
		t.Fatalf("d was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.paths.builds, "c")); !os.IsNotExist(err) {
		t.Fatalf("c survived: %v", err)
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(out)
}
