package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	t.Chdir(project)
	root, fresh, err := resolveRoot("")
	if err != nil || root != project || fresh {
		t.Fatalf("project: root=%q fresh=%v err=%v", root, fresh, err)
	}

	t.Chdir(home)
	root, fresh, err = resolveRoot("")
	want := filepath.Join(home, "bots", "default")
	if err != nil || root != want || !fresh {
		t.Fatalf("home: root=%q fresh=%v err=%v", root, fresh, err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}

	root, fresh, err = resolveRoot("~/bots/other")
	if err != nil || root != filepath.Join(home, "bots", "other") || !fresh {
		t.Fatalf("tilde: root=%q fresh=%v err=%v", root, fresh, err)
	}

	root, fresh, err = resolveRoot(project)
	if err != nil || root != project || fresh {
		t.Fatalf("existing: root=%q fresh=%v err=%v", root, fresh, err)
	}
}

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
