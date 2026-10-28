package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteStarter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	if err := writeStarter(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "bot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `model = "deepseek-flash"`) {
		t.Fatalf("starter = %q", data)
	}
	if strings.Contains(string(data), "[[connection]]") {
		t.Fatalf("wrote a connection for a provider that is not installed: %q", data)
	}
}

func TestWriteStarterAddsInstalledProviders(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fsx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "memoryx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	root := t.TempDir()
	if err := writeStarter(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "bot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, name := range []string{"fsx", "memoryx"} {
		if !strings.Contains(text, `id = "`+name+`"`) || !strings.Contains(text, `command = "`+name+`"`) {
			t.Fatalf("missing %s connection: %q", name, text)
		}
	}
	if strings.Contains(text, `id = "bashx"`) {
		t.Fatalf("wrote bashx without it installed: %q", text)
	}
}

func TestLanAddressIsNotLoopback(t *testing.T) {
	if ip := lanAddress(); ip == "127.0.0.1" || ip == "::1" {
		t.Fatalf("lan address = %q", ip)
	}
}
