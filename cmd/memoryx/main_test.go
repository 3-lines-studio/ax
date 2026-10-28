package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/memory"
)

func TestDescriptors(t *testing.T) {
	if len(descriptors) != 5 {
		t.Fatalf("descriptors = %d", len(descriptors))
	}
	for i, name := range []string{"search", "get", "verify", "upsert", "delete"} {
		if !strings.Contains(descriptors[i], `"name":"`+name+`"`) {
			t.Errorf("descriptor %d does not name %s: %s", i, name, descriptors[i])
		}
	}
	if !strings.Contains(descriptors[3], "Never store secrets") {
		t.Error("upsert descriptor does not carry the storage policy")
	}
}

func TestOpenStoreUsesBotDataState(t *testing.T) {
	data := t.TempDir()
	t.Setenv("BOT_DATA", data)
	t.Setenv("BOT_ROOT", "")
	store, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := store.Upsert(memory.Memory{Subject: "Editor", Summary: "Usa neovim."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "state", "memory", item.ID+".json")); err != nil {
		t.Fatalf("memory file: %v", err)
	}
}

func TestOpenStoreFallsBackToBotRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BOT_ROOT", root)
	t.Setenv("BOT_DATA", "")
	store, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.Root != filepath.Join(root, "state", "memory") {
		t.Fatalf("root = %q", store.Root)
	}
}

func TestFormatListAndOne(t *testing.T) {
	items := []memory.Memory{
		{ID: "a", Subject: "Zona horaria", Summary: "UTC-3.", Category: "profile"},
		{ID: "b", Subject: "Editor", Summary: "Neovim."},
	}
	list := formatList(items)
	if !strings.Contains(list, "`a` [profile]: Zona horaria — UTC-3.") {
		t.Fatalf("list = %q", list)
	}
	one := formatOne(memory.Memory{ID: "a", Subject: "Zona horaria", Summary: "UTC-3.", Body: "Buenos Aires.", SourceURIs: []string{"https://example.com"}})
	for _, want := range []string{"# Zona horaria", "id: `a`", "UTC-3.", "Buenos Aires.", "https://example.com"} {
		if !strings.Contains(one, want) {
			t.Errorf("one missing %q: %s", want, one)
		}
	}
	if formatList(nil) != "No memories." {
		t.Errorf("empty list = %q", formatList(nil))
	}
}
