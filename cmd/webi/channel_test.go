package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
)

// A channel is a bot: its ax process must see the channel root as BOT_DATA, not
// the data root of the host that serves every channel.
func TestChannelGetsItsOwnDataRoot(t *testing.T) {
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
	record := filepath.Join(t.TempDir(), "seen")
	t.Setenv("BOTI_TEST_RECORD", record)
	t.Setenv("BOT_DATA", t.TempDir()) // the host's data root

	axPath := filepath.Join(t.TempDir(), "ax")
	script := `#!/bin/sh
printf '%s' "$BOT_DATA" > "$BOTI_TEST_RECORD"
printf '%s\n' '{"type":"message","message":{"Role":"assistant","Content":"ok"}}' \
  '{"type":"done","outcome":"done"}'
`
	if err := os.WriteFile(axPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, axrun.Config{AXPath: axPath, BotRoot: botRoot}, state, nil, "test")
	thread, err := store.CreateThread(channels[0].ID, "one")
	if err != nil {
		t.Fatal(err)
	}
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"hi"}`, nil); code != 200 {
		t.Fatalf("post message: %d", code)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(record)
		if err == nil {
			if got := string(data); got != botRoot {
				t.Fatalf("BOT_DATA = %q, want %q", got, botRoot)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the ax process never ran")
}
