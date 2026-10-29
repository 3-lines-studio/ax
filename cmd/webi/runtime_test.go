package main

import (
	"strings"
	"testing"
)

// ax's runtime section reads these to know which channel/thread/user it is
// serving; the channel prompt is instructions and must reach it too.
func TestRunEnvCarriesTheRunContext(t *testing.T) {
	env := runEnv(
		Thread{ID: "t1", Title: "hola"},
		Channel{ID: "c1", Name: "general", Workdir: "/w", Prompt: "be brief"},
		"  Don  ",
	)
	got := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		got[key] = value
	}
	want := map[string]string{
		"AX_WEB_THREAD":       "t1",
		"AX_WEB_THREAD_TITLE": "hola",
		"AX_WEB_CHANNEL":      "c1",
		"AX_WEB_CHANNEL_NAME": "general",
		"AX_WEB_USER":         "Don",
		"AX_WEB_PROMPT":       "be brief",
		"BOT_DATA":            "/w",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q (env %v)", key, got[key], value, env)
		}
	}
}

func TestRunEnvOmitsEmptyFields(t *testing.T) {
	env := runEnv(Thread{ID: "t1"}, Channel{ID: "c1", Workdir: "/w"}, "")
	for _, entry := range env {
		if strings.HasPrefix(entry, "AX_WEB_USER=") || strings.HasPrefix(entry, "AX_WEB_PROMPT=") ||
			strings.HasPrefix(entry, "AX_WEB_THREAD_TITLE=") || strings.HasPrefix(entry, "AX_WEB_CHANNEL_NAME=") {
			t.Errorf("empty field leaked: %q", entry)
		}
	}
}
