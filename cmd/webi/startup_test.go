package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteStarter(t *testing.T) {
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
}

func TestLanAddressIsNotLoopback(t *testing.T) {
	if ip := lanAddress(); ip == "127.0.0.1" || ip == "::1" {
		t.Fatalf("lan address = %q", ip)
	}
}
