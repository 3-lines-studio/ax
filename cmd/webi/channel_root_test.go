package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
)

// One bot.toml serves every channel: a channel folder without one inherits the
// host config; a folder with one is a standalone bot and keeps overriding it.
func TestChannelFallsBackToHostBotConfig(t *testing.T) {
	host := t.TempDir()
	channelDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "bot.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: axrun.Config{BotRoot: host}}
	channel := Channel{ID: "c", Workdir: channelDir}
	if got := srv.channelRoot(channel); got != host {
		t.Fatalf("root = %q, want host %q", got, host)
	}
	paths := srv.channelPaths(channel)
	if paths.Root != host || paths.DataRoot != channelDir {
		t.Fatalf("paths = %+v", paths)
	}

	if err := os.WriteFile(filepath.Join(channelDir, "bot.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := srv.channelRoot(channel); got != channelDir {
		t.Fatalf("own bot.toml root = %q, want %q", got, channelDir)
	}
}

func TestRunUsesHostBotRootAndChannelData(t *testing.T) {
	host := t.TempDir()
	channelDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "bot.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "seen")
	t.Setenv("BOTI_TEST_RECORD", record)
	fake := filepath.Join(t.TempDir(), "ax")
	script := `#!/bin/sh
printf '%s\n' "$*" > "$BOTI_TEST_RECORD.args"
printf '%s\n' "$BOT_DATA" > "$BOTI_TEST_RECORD.data"
printf '%s\n' '{"type":"message","message":{"Role":"assistant","Content":"ok"}}' '{"type":"done","outcome":"done"}'
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	store, err := NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutChannel(Channel{ID: "other", Name: "other", Workdir: channelDir, Created: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	thread, err := store.CreateThread("other", "one")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, axrun.Config{AXPath: fake, BotRoot: host}, state, nil, "test")
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"hi"}`, nil); code != 200 {
		t.Fatalf("post message: %d", code)
	}
	waitIdle(t, srv, thread.ID)

	args, err := os.ReadFile(record + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-C "+host) {
		t.Fatalf("args = %q, want -C %s", args, host)
	}
	data, err := os.ReadFile(record + ".data")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != channelDir {
		t.Fatalf("BOT_DATA = %q, want %q", got, channelDir)
	}
}
