package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/axrun"
)

func TestParseArtifactOutput(t *testing.T) {
	spec, ok := parseArtifactOutput(`{"artifact":{"id":"ab12cd","name":"report.csv","media_type":"text/csv","size":42}}`)
	if !ok || spec.Name != "report.csv" || spec.ID != "ab12cd" || spec.Size != 42 {
		t.Fatalf("spec=%+v ok=%v", spec, ok)
	}
	for _, bad := range []string{"", "ok", `{}`, `{"artifact":{"id":"../x","name":"a"}}`, `{"artifact":{"id":"ab","name":""}}`} {
		if _, ok := parseArtifactOutput(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestArtifactPathStaysInsideThreadDir(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := store.ArtifactsDir("t1")
	if err := os.MkdirAll(filepath.Join(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abc-notes.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, ok := store.ArtifactPath("t1", "abc")
	if !ok || filepath.Base(path) != "abc-notes.txt" {
		t.Fatalf("path=%q ok=%v", path, ok)
	}
	for _, id := range []string{"", "..", "abcd", "abc/../abc", "ABC"} {
		if _, ok := store.ArtifactPath("t1", id); ok {
			t.Errorf("resolved %q", id)
		}
	}
}

func TestThreadArtifactIsServedAndAnnounced(t *testing.T) {
	fake, _ := fakeAX(t)
	srv, channel := newTestServer(t, fake)
	var thread Thread
	if code := call(t, srv, "POST", "/api/channels/"+channel+"/threads", `{"title":""}`, &thread); code != 200 {
		t.Fatalf("create thread code=%d", code)
	}
	dir := srv.store.ArtifactsDir(thread.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beef-report.csv"), []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := srv.store.ArtifactPath(thread.ID, "beef")
	if !ok {
		t.Fatal("artifact not found")
	}
	if got != filepath.Join(dir, "beef-report.csv") {
		t.Fatalf("path=%q", got)
	}

	// The run must hand attachx a directory to copy into.
	env := runEnv(thread, srv.store.Channels()[0], "", srv.store.ArtifactsDir(thread.ID))
	if !containsEntry(env, "AX_ARTIFACT_DIR="+dir) {
		t.Fatalf("AX_ARTIFACT_DIR missing from %v", env)
	}

	// A tool_done carrying an attachx payload becomes an artifact event.
	var seen []map[string]any
	srv.mu.Lock()
	ch := make(chan []byte, 8)
	srv.subs[ch] = struct{}{}
	srv.mu.Unlock()
	defer func() {
		srv.mu.Lock()
		delete(srv.subs, ch)
		srv.mu.Unlock()
	}()
	output, _ := json.Marshal(map[string]any{"artifact": map[string]any{
		"id": "beef", "name": "report.csv", "media_type": "text/csv", "size": 4,
	}})
	srv.forwardEvent(thread.ID, axrun.Event{Type: "tool_done", Output: string(output)})
	close(ch)
	for data := range ch {
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("event: %v", err)
		}
		seen = append(seen, event)
	}
	if len(seen) != 1 || seen[0]["type"] != "artifact" {
		t.Fatalf("events=%v", seen)
	}
}

func containsEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func TestReadSessionRestoresArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"message","message":{"role":"assistant","content":"done"},"at":1000}`,
		`{"type":"tool_result","output":"{\"artifact\":{\"id\":\"beef\",\"name\":\"report.csv\",\"media_type\":\"text/csv\",\"size\":4}}","at":2000}`,
		`{"type":"tool_result","output":"plain text","at":3000}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := readSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1]["t"] != "artifacts" {
		t.Fatalf("events=%v", events)
	}
	items, ok := events[1]["artifacts"].([]map[string]any)
	if !ok || len(items) != 1 || items[0]["name"] != "report.csv" {
		t.Fatalf("artifacts=%v", events[1]["artifacts"])
	}
}
