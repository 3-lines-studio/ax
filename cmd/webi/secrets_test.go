package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tool that asks for a secret mid-run: webi keeps the request, stores the
// value in the channel's vault scope and hands it to the live ax, which retries.
func TestSecretRequestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "secret.txt")
	// The fake ax announces a secret_request, then reports whatever control line
	// webi sends back.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"type\":\"secret_request\",\"id\":\"c1\",\"tool\":\"demo.need\",\"name\":\"DEMO_TOKEN\",\"description\":\"demo\"}'\n" +
		"read line\n" +
		"printf '%s' \"$line\" > " + record + "\n" +
		"printf '%s\\n' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	axPath := filepath.Join(dir, "ax")
	if err := os.WriteFile(axPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, channel := newTestServer(t, axPath)
	if code := call(t, srv, "POST", "/api/vault/init", `{"pin":"long-enough","confirm":"long-enough"}`, nil); code != 200 {
		t.Fatalf("init code=%d", code)
	}
	channels := srv.store.Channels()
	for i := range channels {
		if channels[i].ID == channel {
			channels[i].VaultScope = "team"
			if err := srv.store.PutChannel(channels[i]); err != nil {
				t.Fatal(err)
			}
		}
	}

	var thread Thread
	if code := call(t, srv, "POST", "/api/channels/"+channel+"/threads", `{"title":""}`, &thread); code != 200 {
		t.Fatalf("create thread code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"do it"}`, nil); code != 200 {
		t.Fatalf("message code=%d", code)
	}

	// The request shows up in the state the UI polls.
	var state struct {
		Pending []pendingSecret `json:"pending_secret"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		call(t, srv, "GET", "/api/state", "", &state)
		if len(state.Pending) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pending secret: %+v", state.Pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
	request := state.Pending[0]
	if request.Name != "DEMO_TOKEN" || request.Scope != "team" || request.Thread != thread.ID {
		t.Fatalf("request = %+v", request)
	}

	var answered struct {
		OK bool `json:"ok"`
	}
	if code := call(t, srv, "POST", "/api/vault/secret", `{"id":"`+request.ID+`","value":"s3cret"}`, &answered); code != 200 || !answered.OK {
		t.Fatalf("answer code=%d %+v", code, answered)
	}

	// ax got the control line.
	deadline = time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(record)
		if err == nil && strings.Contains(string(data), `"type":"secret"`) && strings.Contains(string(data), "s3cret") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the secret never reached ax: %v %q", err, data)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// And the vault kept it for the next run.
	value, err := srv.vault.Reveal("team", "DEMO_TOKEN", "long-enough")
	if err != nil || value != "s3cret" {
		t.Fatalf("vault value = %q err=%v", value, err)
	}
	// The request is gone once answered.
	call(t, srv, "GET", "/api/state", "", &state)
	if len(state.Pending) != 0 {
		t.Fatalf("pending after answer = %+v", state.Pending)
	}
}

func TestSecretSkip(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "secret.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"type\":\"secret_request\",\"id\":\"c1\",\"name\":\"DEMO_TOKEN\"}'\n" +
		"read line\n" +
		"printf '%s' \"$line\" > " + record + "\n" +
		"printf '%s\\n' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	axPath := filepath.Join(dir, "ax")
	if err := os.WriteFile(axPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, channel := newTestServer(t, axPath)
	var thread Thread
	if code := call(t, srv, "POST", "/api/channels/"+channel+"/threads", `{"title":""}`, &thread); code != 200 {
		t.Fatalf("create thread code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"go"}`, nil); code != 200 {
		t.Fatalf("message code=%d", code)
	}
	var state struct {
		Pending []pendingSecret `json:"pending_secret"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		call(t, srv, "GET", "/api/state", "", &state)
		if len(state.Pending) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pending secret: %+v", state.Pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code := call(t, srv, "POST", "/api/vault/secret", `{"id":"`+state.Pending[0].ID+`","skip":true}`, nil); code != 200 {
		t.Fatalf("skip code=%d", code)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(record)
		if err == nil && strings.Contains(string(data), `"skip":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the skip never reached ax: %v %q", err, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
