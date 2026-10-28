package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppDataRootFor(t *testing.T) {
	if got := appDataRootFor("linux", "/home/u", "/xdg", ""); got != "/xdg/ax" {
		t.Fatalf("linux = %q", got)
	}
	if got := appDataRootFor("linux", "/home/u", "", ""); got != "/home/u/.local/share/ax" {
		t.Fatalf("linux fallback = %q", got)
	}
	if got := appDataRootFor("darwin", "/Users/u", "", ""); got != "/Users/u/Library/Application Support/ax" {
		t.Fatalf("darwin = %q", got)
	}
	if got := appDataRootFor("windows", `C:\Users\u`, "", `C:\Users\u\AppData\Local`); got != filepath.Join(`C:\Users\u\AppData\Local`, "ax") {
		t.Fatalf("windows = %q", got)
	}
}

func TestResolveRoot(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("BOT_ROOT", "")
	t.Setenv("BOT_DATA", "")

	root, fresh, err := resolveRoot("")
	want := filepath.Join(data, "ax")
	if err != nil || root != want || !fresh {
		t.Fatalf("default: root=%q fresh=%v err=%v", root, fresh, err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err = resolveRoot(""); err != nil || fresh {
		t.Fatalf("second call: fresh=%v err=%v", fresh, err)
	}

	bot := filepath.Join(t.TempDir(), "bot")
	t.Setenv("BOT_ROOT", bot)
	root, fresh, err = resolveRoot("")
	if err != nil || root != bot || !fresh {
		t.Fatalf("BOT_ROOT: root=%q fresh=%v err=%v", root, fresh, err)
	}

	project := t.TempDir()
	root, fresh, err = resolveRoot(project)
	if err != nil || root != project || fresh {
		t.Fatalf("explicit: root=%q fresh=%v err=%v", root, fresh, err)
	}

	root, fresh, err = resolveRoot("~/project")
	if err != nil || root != filepath.Join(home, "project") || !fresh {
		t.Fatalf("tilde: root=%q fresh=%v err=%v", root, fresh, err)
	}
}
