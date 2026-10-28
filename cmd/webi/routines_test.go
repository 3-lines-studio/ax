package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
)

func routineServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	t.Setenv("BOT_DATA", "")
	if err := os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "routines", "hello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routine.toml"), []byte("description = \"say hello\"\nentry = \"./run.sh\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho hello routine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: axrun.Config{BotRoot: root}}
}

func TestRoutinesAPIListRunAndLog(t *testing.T) {
	srv := routineServer(t)

	rec := httptest.NewRecorder()
	srv.handleRoutines(rec, httptest.NewRequest("GET", "/api/routines", nil))
	if rec.Code != 200 {
		t.Fatalf("list code = %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Routines []routineView `json:"routines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Routines) != 1 || list.Routines[0].Name != "hello" || list.Routines[0].Enabled {
		t.Fatalf("list = %+v", list)
	}

	req := httptest.NewRequest("POST", "/api/routines/hello/run", nil)
	req.SetPathValue("name", "hello")
	rec = httptest.NewRecorder()
	srv.handleRunRoutine(rec, req)
	if rec.Code != 200 {
		t.Fatalf("run code = %d %s", rec.Code, rec.Body)
	}
	var run struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
		Log string `json:"log"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Run.Status != "ok" || !strings.Contains(run.Log, "hello routine") {
		t.Fatalf("run = %+v", run)
	}

	req = httptest.NewRequest("GET", "/api/routines/hello/log", nil)
	req.SetPathValue("name", "hello")
	rec = httptest.NewRecorder()
	srv.handleRoutineLog(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello routine") {
		t.Fatalf("log code = %d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest("POST", "/api/routines/missing/run", nil)
	req.SetPathValue("name", "missing")
	rec = httptest.NewRecorder()
	srv.handleRunRoutine(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing code = %d", rec.Code)
	}
}
func writeRoutineAt(t *testing.T, root, name, manifest, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "routines", name)
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

func TestTailFileCapsOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.log")
	body := strings.Repeat("a", maxRoutineLog+5000) + "end"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := tailFile(path)
	if len(got) != maxRoutineLog || !strings.HasSuffix(got, "end") {
		t.Fatalf("tail len = %d suffix = %q", len(got), got[len(got)-3:])
	}
	if tailFile(filepath.Join(t.TempDir(), "missing")) != "" {
		t.Fatal("missing file should read as empty")
	}
}

func TestRoutinesAcrossChannelsAndHistory(t *testing.T) {
	rootOne := t.TempDir()
	rootTwo := t.TempDir()
	writeRoutineAt(t, rootOne, "one", "entry = \"./run.sh\"\n", "#!/bin/sh\necho one\n")
	writeRoutineAt(t, rootTwo, "two", "entry = \"./run.sh\"\n", "#!/bin/sh\necho two\n")

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	channels := store.Channels()
	channels[0].Workdir = rootOne
	if err := store.PutChannel(channels[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChannel(Channel{ID: "two", Name: "two", Workdir: rootTwo, Created: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, cfg: axrun.Config{BotRoot: rootOne}}

	rec := httptest.NewRecorder()
	srv.handleRoutines(rec, httptest.NewRequest("GET", "/api/routines", nil))
	if rec.Code != 200 {
		t.Fatalf("all routines code=%d %s", rec.Code, rec.Body)
	}
	var list struct {
		Routines []routineView `json:"routines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Routines) != 2 {
		t.Fatalf("routines = %+v", list.Routines)
	}

	req := httptest.NewRequest("POST", "/api/routines/two/run?channel=two", nil)
	req.SetPathValue("name", "two")
	rec = httptest.NewRecorder()
	srv.handleRunRoutine(rec, req)
	if rec.Code != 200 {
		t.Fatalf("run channel two code=%d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest("GET", "/api/routines/two/runs?channel=two", nil)
	req.SetPathValue("name", "two")
	rec = httptest.NewRecorder()
	srv.handleRoutineRuns(rec, req)
	if rec.Code != 200 {
		t.Fatalf("runs code=%d %s", rec.Code, rec.Body)
	}
	var runs struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil || len(runs.Runs) != 1 {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}

	req = httptest.NewRequest("GET", "/api/routines/two/runs/"+runs.Runs[0].ID+"?channel=two", nil)
	req.SetPathValue("name", "two")
	req.SetPathValue("run", runs.Runs[0].ID)
	rec = httptest.NewRecorder()
	srv.handleRoutineRunLog(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "two") {
		t.Fatalf("run log code=%d %s", rec.Code, rec.Body)
	}
}

func TestRoutineSchedulerRunsEveryChannel(t *testing.T) {
	rootOne := t.TempDir()
	rootTwo := t.TempDir()
	writeRoutineAt(t, rootOne, "one", "entry = \"./run.sh\"\nschedule = \"* * * * *\"\nenabled = true\n", "#!/bin/sh\necho one > \"$ROUTINE_STATE/seen\"\n")
	writeRoutineAt(t, rootTwo, "two", "entry = \"./run.sh\"\nschedule = \"* * * * *\"\nenabled = true\n", "#!/bin/sh\necho two > \"$ROUTINE_STATE/seen\"\n")
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	channels := store.Channels()
	channels[0].Workdir = rootOne
	if err := store.PutChannel(channels[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChannel(Channel{ID: "two", Name: "two", Workdir: rootTwo, Created: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	sched := newRoutineScheduler(store)
	sched.tick(context.Background(), time.Now())
	for _, root := range []string{rootOne, rootTwo} {
		state := filepath.Join(root, "state", "routines")
		deadline := time.Now().Add(3 * time.Second)
		for {
			found := false
			_ = filepath.Walk(state, func(path string, info os.FileInfo, err error) error {
				if err == nil && info != nil && info.Name() == "seen" {
					found = true
				}
				return nil
			})
			if found {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("routine in %s never ran", root)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
