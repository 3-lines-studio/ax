package routine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
)

func testPaths(t *testing.T) botdir.Paths {
	t.Helper()
	root := t.TempDir()
	paths := botdir.Paths{
		Root:      root,
		DataRoot:  root,
		State:     filepath.Join(root, "state"),
		Workspace: filepath.Join(root, "workspace"),
		Routines:  filepath.Join(root, "routines"),
	}
	for _, dir := range []string{paths.Workspace, paths.Routines} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func writeRoutine(t *testing.T, paths botdir.Paths, name, manifest, script string) {
	t.Helper()
	dir := filepath.Join(paths.Routines, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routine.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunWritesStateAndLog(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "hello", "entry = \"./run.sh\"\ntimeout = \"2s\"\n", `#!/bin/sh
printf '%s %s\n' "$ROUTINE_NAME" "$ROUTINE_TRIGGER" > "$ROUTINE_STATE/seen"
echo "hello from routine"
`)
	set, err := Open(paths)
	if err != nil || len(set.Items) != 1 {
		t.Fatalf("open = %+v err=%v", set, err)
	}
	item, err := set.Find("hello")
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(paths)
	result, err := runner.Run(context.Background(), item, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Log, "runs") {
		t.Fatalf("log path = %q", result.Log)
	}
	logText, err := os.ReadFile(result.Log)
	if err != nil || !strings.Contains(string(logText), "hello from routine") {
		t.Fatalf("log = %q err=%v", logText, err)
	}
	seen, err := os.ReadFile(filepath.Join(paths.State, "routines", "hello", "seen"))
	if err != nil || strings.TrimSpace(string(seen)) != "hello manual" {
		t.Fatalf("seen = %q err=%v", seen, err)
	}
	last, err := runner.LastRun("hello")
	if err != nil || last.ID != result.ID || last.Status != "ok" {
		t.Fatalf("last = %+v err=%v", last, err)
	}
}

func TestRunsAndRunLog(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "hello", "entry = \"./run.sh\"\n", "#!/bin/sh\necho hello\n")
	set, _ := Open(paths)
	item, _ := set.Find("hello")
	runner := NewRunner(paths)
	first, err := runner.Run(context.Background(), item, "manual")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), item, "cron")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := runner.Runs("hello", 10)
	if err != nil || len(runs) != 2 || runs[0].ID != second.ID || runs[1].ID != first.ID {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}
	result, log, err := runner.RunLog("hello", first.ID)
	if err != nil || result.ID != first.ID || !strings.Contains(log, "hello") {
		t.Fatalf("run log = %+v %q err=%v", result, log, err)
	}
	if _, _, err := runner.RunLog("hello", "../x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("escape = %v", err)
	}
}

func TestStaleRunningIsReconciled(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "hello", "entry = \"./run.sh\"\n", "#!/bin/sh\necho hello\n")
	runner := NewRunner(paths)
	state := runner.stateDir("hello")
	stale := filepath.Join(state, "runs", "20200101T000000-deadbeef")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "output.log"), []byte("partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.writeResult(stale, RunResult{
		ID: "20200101T000000-deadbeef", Name: "hello", Trigger: "cron",
		Status: "running", ExitCode: -1, StartedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	set, _ := Open(paths)
	item, _ := set.Find("hello")
	if _, err := runner.Run(context.Background(), item, "manual"); err != nil {
		t.Fatal(err)
	}
	result, _, err := runner.RunLog("hello", "20200101T000000-deadbeef")
	if err != nil || result.Status != "interrupted" || !strings.Contains(result.Error, "interrupted") {
		t.Fatalf("stale run = %+v err=%v", result, err)
	}
}

func TestTimeoutAndFailure(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "slow", "entry = \"./run.sh\"\ntimeout = \"100ms\"\n", "#!/bin/sh\nsleep 5\n")
	set, _ := Open(paths)
	item, _ := set.Find("slow")
	result, err := NewRunner(paths).Run(context.Background(), item, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "timeout" {
		t.Fatalf("timeout result = %+v", result)
	}

	writeRoutine(t, paths, "fail", "entry = \"./run.sh\"\n", "#!/bin/sh\nexit 3\n")
	set, _ = Open(paths)
	item, _ = set.Find("fail")
	result, err = NewRunner(paths).Run(context.Background(), item, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.ExitCode != 3 {
		t.Fatalf("fail result = %+v", result)
	}
}

func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "tree", "entry = \"./run.sh\"\ntimeout = \"200ms\"\n", `#!/bin/sh
( sleep 1; echo late > "$ROUTINE_STATE/late" ) &
sleep 5
`)
	set, _ := Open(paths)
	item, _ := set.Find("tree")
	runner := NewRunner(paths)
	result, err := runner.Run(context.Background(), item, "manual")
	if err != nil || result.Status != "timeout" {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(runner.stateDir("tree"), "late")); err == nil {
		t.Fatal("a child of the routine survived the timeout")
	}
}

func TestConcurrentRunIsRejected(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "slow", "entry = \"./run.sh\"\ntimeout = \"2s\"\n", "#!/bin/sh\nsleep 1\n")
	set, _ := Open(paths)
	item, _ := set.Find("slow")
	runner := NewRunner(paths)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), item, "manual")
		done <- err
	}()
	lock := filepath.Join(paths.State, "routines", "slow", "lock")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(lock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first run never took the lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := runner.Run(context.Background(), item, "manual"); !errors.Is(err, ErrRunning) {
		t.Fatalf("second run = %v, want ErrRunning", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDisabledAndScheduled(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "off", "entry = \"./run.sh\"\nschedule = \"* * * * *\"\n", "#!/bin/sh\ntrue\n")
	writeRoutine(t, paths, "on", "entry = \"./run.sh\"\nschedule = \"0 8 * * *\"\nenabled = true\ntimezone = \"UTC\"\n", "#!/bin/sh\ntrue\n")
	set, err := Open(paths)
	if err != nil || len(set.Items) != 2 {
		t.Fatalf("open = %+v err=%v", set, err)
	}
	scheduler := NewScheduler(paths)
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	if due := scheduler.Due(now); len(due) != 1 || due[0].Name != "on" {
		t.Fatalf("due = %+v", due)
	}
	if due := scheduler.Due(now); len(due) != 0 {
		t.Fatalf("same minute due = %+v", due)
	}
	if due := scheduler.Due(now.Add(time.Minute)); len(due) != 0 {
		t.Fatalf("next minute due = %+v", due)
	}
}

func TestKeepRunsPrunesOldRuns(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "hello", "entry = \"./run.sh\"\nkeep_runs = 2\n", "#!/bin/sh\necho hi\n")
	set, _ := Open(paths)
	item, _ := set.Find("hello")
	if item.KeepRuns != 2 {
		t.Fatalf("KeepRuns = %d", item.KeepRuns)
	}
	runner := NewRunner(paths)
	for i := 0; i < 4; i++ {
		if _, err := runner.Run(context.Background(), item, "manual"); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := runner.Runs("hello", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}
	entries, err := os.ReadDir(filepath.Join(runner.stateDir("hello"), "runs"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("run dirs = %d err=%v", len(entries), err)
	}
}

func TestInvalidManifest(t *testing.T) {
	paths := testPaths(t)
	writeRoutine(t, paths, "escape", "entry = \"../outside.sh\"\n", "#!/bin/sh\ntrue\n")
	if _, err := Open(paths); err == nil {
		t.Fatal("accepted an entry outside the routine directory")
	}
	other := testPaths(t)
	writeRoutine(t, other, "negative", "entry = \"./run.sh\"\nkeep_runs = -1\n", "#!/bin/sh\ntrue\n")
	if _, err := Open(other); err == nil {
		t.Fatal("accepted a negative keep_runs")
	}
}
