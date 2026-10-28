package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
