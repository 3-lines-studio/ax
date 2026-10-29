package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
	"github.com/3-lines-studio/ax/internal/memory"
)

func TestMemoryAPIIsPerChannel(t *testing.T) {
	rootOne := t.TempDir()
	rootTwo := t.TempDir()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	channels := store.Channels()
	channels[0].Workdir = rootOne
	if err := store.PutChannel(channels[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChannel(Channel{ID: "two", Name: "two", Workdir: rootTwo, Created: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	one := memory.New(filepath.Join(rootOne, "state", "memory"))
	if _, _, err := one.Upsert(memory.Memory{Subject: "Editor", Summary: "Usa neovim.", Category: "profile"}); err != nil {
		t.Fatal(err)
	}
	two := memory.New(filepath.Join(rootTwo, "state", "memory"))
	if _, _, err := two.Upsert(memory.Memory{Subject: "Shell", Summary: "Usa fish."}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, cfg: axrun.Config{BotRoot: t.TempDir()}}

	list := func(channel, query string) []memory.Memory {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleMemoryList(rec, httptest.NewRequest("GET", "/api/memory?channel="+channel+"&q="+query, nil))
		if rec.Code != 200 {
			t.Fatalf("list %s = %d %s", channel, rec.Code, rec.Body)
		}
		var body struct {
			Memories []memory.Memory `json:"memories"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Memories
	}

	if got := list("main", ""); len(got) != 1 || got[0].Subject != "Editor" {
		t.Fatalf("main memories = %+v", got)
	}
	if got := list("two", ""); len(got) != 1 || got[0].Subject != "Shell" {
		t.Fatalf("two memories = %+v", got)
	}
	if got := list("two", "neovim"); len(got) != 0 {
		t.Fatalf("cross-channel search = %+v", got)
	}
	if got := list("main", "neovim"); len(got) != 1 {
		t.Fatalf("main search = %+v", got)
	}

	req := httptest.NewRequest("GET", "/api/memory/"+gotID(t, one), nil)
	req.SetPathValue("id", gotID(t, one))
	rec := httptest.NewRecorder()
	srv.handleMemoryGet(rec, req)
	if rec.Code != 404 {
		t.Fatalf("main memory read from the bot root without a channel = %d", rec.Code)
	}

	req = httptest.NewRequest("DELETE", "/api/memory/"+gotID(t, one)+"?channel=main", nil)
	req.SetPathValue("id", gotID(t, one))
	rec = httptest.NewRecorder()
	srv.handleMemoryDelete(rec, req)
	if rec.Code != 200 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if got := list("main", ""); len(got) != 0 {
		t.Fatalf("after delete = %+v", got)
	}
	if got := list("two", ""); len(got) != 1 {
		t.Fatalf("delete leaked into two = %+v", got)
	}
}

func gotID(t *testing.T, store *memory.Store) string {
	t.Helper()
	items, err := store.List(1)
	if err != nil || len(items) != 1 {
		t.Fatalf("store = %+v err=%v", items, err)
	}
	return items[0].ID
}
