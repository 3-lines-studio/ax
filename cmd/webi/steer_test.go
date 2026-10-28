package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A message sent while the run is going steers it instead of being rejected,
// and images cannot ride along because ax has no way to attach them mid-run.
func TestMessageWhileRunningSteers(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "steer.txt")
	script := "#!/bin/sh\n" +
		"sleep 1\n" +
		"read line\n" +
		"printf '%s' \"$line\" > " + record + "\n" +
		"printf '%s\\n' '{\"type\":\"message\",\"message\":{\"Role\":\"assistant\",\"Content\":\"ok\"}}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	axPath := filepath.Join(dir, "ax")
	if err := os.WriteFile(axPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, channel := newTestServer(t, axPath)

	var thread Thread
	if code := call(t, srv, "POST", "/api/channels/"+channel+"/threads", `{"title":""}`, &thread); code != 200 {
		t.Fatalf("create thread code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"move the fs"}`, nil); code != 200 {
		t.Fatalf("first message code=%d", code)
	}
	var steer struct {
		OK      bool `json:"ok"`
		Steered bool `json:"steered"`
	}
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"and update the docs"}`, &steer); code != 200 || !steer.Steered {
		t.Fatalf("steer code=%d %+v", code, steer)
	}
	// Images cannot be steered: they would need a new prompt, not a user turn.
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"x","images":["nope"]}`, nil); code != 409 {
		t.Fatalf("image steer code=%d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(record)
		if err == nil && strings.Contains(string(data), "and update the docs") {
			if !strings.Contains(string(data), `"type":"steer"`) {
				t.Fatalf("control line = %s", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("steer never reached ax: %v %q", err, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
