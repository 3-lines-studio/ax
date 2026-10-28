package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	return New(root), root
}

func TestUpsertSearchAndDedupe(t *testing.T) {
	store, root := newStore(t)
	first, created, err := store.Upsert(Memory{
		Subject:   "Zona horaria de Don",
		Summary:   "Don vive en Argentina, UTC-3.",
		Body:      "Usar America/Argentina/Buenos_Aires para horarios.",
		Category:  "profile",
		DedupeKey: "profile:timezone",
	})
	if err != nil || !created {
		t.Fatalf("create = %+v created=%v err=%v", first, created, err)
	}
	if first.ID == "" || !validID(first.ID) {
		t.Fatalf("id = %q", first.ID)
	}
	if _, err := os.Stat(filepath.Join(root, first.ID+".json")); err != nil {
		t.Fatal(err)
	}

	items, err := store.Search("zona horaria", 5)
	if err != nil || len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("search = %+v err=%v", items, err)
	}
	items, err = store.Search("UTC-3", 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("body search = %+v err=%v", items, err)
	}

	second, created, err := store.Upsert(Memory{
		Subject:   "Zona horaria de Don",
		Summary:   "Don vive en Argentina (UTC-3), horario de Buenos Aires.",
		Category:  "profile",
		DedupeKey: "profile:timezone",
	})
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("dedupe update = %+v created=%v err=%v", second, created, err)
	}
	all, err := store.List(0)
	if err != nil || len(all) != 1 {
		t.Fatalf("dedupe list = %+v err=%v", all, err)
	}
	if all[0].CreatedAt.IsZero() || all[0].UpdatedAt.Before(all[0].CreatedAt) {
		t.Fatalf("timestamps = %+v", all[0])
	}
}

func TestDeleteRestoreAndForce(t *testing.T) {
	store, root := newStore(t)
	item, _, err := store.Upsert(Memory{Subject: "Editor", Summary: "Usa neovim.", DedupeKey: "tool:editor"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(item.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
	if items, _ := store.Search("neovim", 5); len(items) != 0 {
		t.Fatalf("deleted memory surfaced: %+v", items)
	}

	restored, created, err := store.Upsert(Memory{Subject: "Editor", Summary: "Usa neovim y helix.", DedupeKey: "tool:editor"})
	if err != nil || created || restored.ID != item.ID {
		t.Fatalf("restore = %+v created=%v err=%v", restored, created, err)
	}
	if err := store.Delete(item.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, item.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("force delete left the file: %v", err)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	store, _ := newStore(t)
	older, _, err := store.Upsert(Memory{Subject: "One", Summary: "first"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	newer, _, err := store.Upsert(Memory{Subject: "Two", Summary: "second"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(1)
	if err != nil || len(items) != 1 || items[0].ID != newer.ID {
		t.Fatalf("list = %+v err=%v", items, err)
	}
	items, err = store.List(0)
	if err != nil || len(items) != 2 || items[0].ID != newer.ID || items[1].ID != older.ID {
		t.Fatalf("list all = %+v err=%v", items, err)
	}
}

func TestValidationAndInvalidIDs(t *testing.T) {
	store, _ := newStore(t)
	for _, item := range []Memory{
		{Summary: "missing subject"},
		{Subject: "missing summary"},
		{Subject: "long", Summary: strings.Repeat("x", 2001)},
	} {
		if _, _, err := store.Upsert(item); err == nil {
			t.Fatalf("accepted invalid memory: %+v", item)
		}
	}
	if _, err := store.Get("../secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("path escape = %v", err)
	}
	if err := store.Delete("../secret", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete escape = %v", err)
	}
}

func TestEmptyStore(t *testing.T) {
	store, _ := newStore(t)
	if items, err := store.List(0); err != nil || len(items) != 0 {
		t.Fatalf("list = %+v err=%v", items, err)
	}
	if items, err := store.Search("anything", 5); err != nil || len(items) != 0 {
		t.Fatalf("search = %+v err=%v", items, err)
	}
}
