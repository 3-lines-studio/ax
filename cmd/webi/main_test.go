package main

import (
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

func newTestServer(t *testing.T, axPath string) (*Server, string) {
	t.Helper()
	state := t.TempDir()
	store, err := NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	botRoot := t.TempDir()
	channels := store.Channels()
	channels[0].Workdir = botRoot
	if err := store.PutChannel(channels[0]); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, axrun.Config{AXPath: axPath, BotRoot: botRoot}, state, nil, "test")
	return srv, store.Channels()[0].ID
}

func call(t *testing.T, srv *Server, method, path, body string, target any) int {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if target != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
			t.Fatalf("%s %s: %v (%s)", method, path, err, rec.Body.String())
		}
	}
	return rec.Code
}

func TestStateThreadsAndMessages(t *testing.T) {
	fake, dir := fakeAX(t)
	srv, channel := newTestServer(t, fake)
	var state struct {
		Channels []struct {
			ID      string `json:"id"`
			Workdir string `json:"workdir"`
			Threads []struct {
				ID string `json:"id"`
			} `json:"threads"`
		} `json:"channels"`
	}
	if code := call(t, srv, "GET", "/api/state", "", &state); code != 200 || len(state.Channels) != 1 {
		t.Fatalf("state code=%d %+v", code, state)
	}
	var thread Thread
	if code := call(t, srv, "POST", "/api/channels/"+channel+"/threads", `{"title":""}`, &thread); code != 200 || thread.ID == "" {
		t.Fatalf("create thread code=%d %+v", code, thread)
	}
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"hi"}`, nil); code != 200 {
		t.Fatalf("post message code=%d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got struct {
			Messages []map[string]any `json:"messages"`
			Running  bool             `json:"running"`
		}
		call(t, srv, "GET", "/api/threads/"+thread.ID, "", &got)
		if !got.Running && len(got.Messages) >= 2 {
			if got.Messages[0]["t"] != "user" {
				t.Fatalf("messages = %+v", got.Messages)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never finished: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = dir
}

func TestReadSessionProjectsMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := `{"type":"run_start"}
{"type":"message","message":{"Role":"user","Content":"hello"}}
{"type":"message","message":{"Role":"assistant","Content":"","ToolCalls":[{"ID":"c"}]}}
{"type":"message","message":{"Role":"tool","Content":"out"}}
{"type":"message","message":{"Role":"assistant","Content":"answer"}}
{"type":"compaction","upto":2,"summary":"s"}
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
	if events[0]["t"] != "user" || events[1]["text"] != "answer" || events[2]["t"] != "compaction" {
		t.Fatalf("events = %+v", events)
	}
	if _, err := readSession(filepath.Join(t.TempDir(), "missing.jsonl")); err != nil {
		t.Errorf("missing session: %v", err)
	}
}

func fakeAX(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ax")
	script := `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    --session) shift; SESSION="$1" ;;
  esac
  shift
done
if [ -n "$SESSION" ]; then
  printf '%s\n' '{"type":"message","message":{"Role":"user","Content":"hi"}}' \
    '{"type":"message","message":{"Role":"assistant","Content":"Hello"}}' > "$SESSION"
fi
printf '%s\n' '{"type":"assistant_delta","text":"Hel"}' \
  '{"type":"message","message":{"Role":"assistant","Content":"Hello"}}' \
  '{"type":"done","outcome":"done"}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

var _ = http.StatusOK
