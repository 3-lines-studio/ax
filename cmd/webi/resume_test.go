package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A thread whose ax process died keeps a run_start with no run_end; webi has to
// pick it up on its own, without a user opening the thread.
func TestResumeLoopPicksUpInterruptedThreads(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > " + record + "\n" +
		"printf '%s\\n' '{\"type\":\"message\",\"message\":{\"Role\":\"assistant\",\"Content\":\"continued\"}}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	axPath := filepath.Join(dir, "ax")
	if err := os.WriteFile(axPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, channel := newTestServer(t, axPath)

	var thread Thread
	if code := call(t, srv, "POST", "/api/channels/"+channel+"/threads", `{"title":""}`, &thread); code != 200 {
		t.Fatalf("create thread code=%d", code)
	}
	session := srv.store.SessionPath(thread.ID)
	if err := os.MkdirAll(filepath.Dir(session), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := strings.Join([]string{
		`{"type":"run_start","run_id":"r1"}`,
		`{"type":"message","message":{"Role":"user","Content":"do the work"}}`,
		`{"type":"tool_intent","call":{"ID":"c1"},"target":"fsx.write"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(session, []byte(entries), 0o644); err != nil {
		t.Fatal(err)
	}

	srv.resumeOpenRuns()
	defer waitIdle(t, srv, thread.ID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(record)
		if err == nil {
			if !strings.Contains(string(data), "--resume") || !strings.Contains(string(data), "--session") {
				t.Fatalf("ax was started without --resume: %s", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the interrupted run was never resumed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A finished thread is not resumed again.
	if err := os.WriteFile(session, []byte(entries+`{"type":"run_end","run_id":"r1","outcome":"done"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	srv.resumeOpenRuns()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("a closed session was resumed")
	}
}

func TestScanOpenRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"run_start"}`+"\n"+`{"type":"run_end"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if open, err := scanOpenRun(path); err != nil || open {
		t.Fatalf("closed session: open=%v err=%v", open, err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"run_start"}`+"\n"+`not json`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if open, err := scanOpenRun(path); err != nil || !open {
		t.Fatalf("open session: open=%v err=%v", open, err)
	}
	if open, err := scanOpenRun(filepath.Join(dir, "missing.jsonl")); err != nil || open {
		t.Fatalf("missing session: open=%v err=%v", open, err)
	}
}

// The resume turn belongs to ax, not to the user: the projection renders it as
// a divider instead of a message bubble.
func TestReadSessionHidesTheResumeTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := `{"type":"message","message":{"Role":"user","Content":"do the work"}}
{"type":"message","resume":true,"message":{"Role":"user","Content":"Resume the interrupted run from where it stopped."}}
{"type":"message","message":{"Role":"assistant","Content":"done"}}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	events, err := readSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	if events[1]["t"] != "resume" {
		t.Fatalf("resume turn = %+v", events[1])
	}
	if events[0]["t"] != "user" || events[2]["t"] != "assistant" {
		t.Fatalf("events = %+v", events)
	}
}
