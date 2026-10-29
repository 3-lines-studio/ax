package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestUpdateChannelWorkdir(t *testing.T) {
	fake, _ := fakeAX(t)
	srv, channelID := newTestServer(t, fake)
	next := t.TempDir()
	payload, _ := json.Marshal(map[string]string{"workdir": next})
	var got Channel
	if code := call(t, srv, "PATCH", "/api/channels/"+channelID, string(payload), &got); code != 200 {
		t.Fatalf("patch workdir: %d", code)
	}
	if got.Workdir != next {
		t.Fatalf("workdir = %q, want %q", got.Workdir, next)
	}
	var state struct {
		Channels []struct {
			Workdir string `json:"workdir"`
		} `json:"channels"`
	}
	if code := call(t, srv, "GET", "/api/state", "", &state); code != 200 || len(state.Channels) != 1 || state.Channels[0].Workdir != next {
		t.Fatalf("state workdir = %+v", state)
	}

	for _, dir := range []string{filepath.Join(t.TempDir(), "missing"), "relative/path"} {
		payload, _ := json.Marshal(map[string]string{"workdir": dir})
		if code := call(t, srv, "PATCH", "/api/channels/"+channelID, string(payload), nil); code != 400 {
			t.Fatalf("workdir %q = %d, want 400", dir, code)
		}
	}
	if c, ok := srv.store.Channel(channelID); !ok || c.Workdir != next {
		t.Fatalf("workdir after bad patches = %+v", c)
	}
}

func TestCreateChannelNeedsAnAbsoluteWorkdir(t *testing.T) {
	fake, _ := fakeAX(t)
	srv, _ := newTestServer(t, fake)
	payload, _ := json.Marshal(map[string]string{"name": "x", "workdir": "relative/path"})
	if code := call(t, srv, "POST", "/api/channels", string(payload), nil); code != 400 {
		t.Fatalf("relative create = %d, want 400", code)
	}
}
