package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		description string
		body        string
	}{
		{"standard", "---\ndescription: My skill\n---\nThe body", "My skill", "The body"},
		{"trim leading newlines", "---\ndescription: A\n---\n\n\nBody", "A", "Body"},
		{"no description falls back to first body line", "---\n---\nFirst body line\nrest", "First body line", "First body line\nrest"},
		{"plain fallback", "Description here\nbody", "Description here", "Description here\nbody"},
		{"empty content", "", "", ""},
	} {
		description, body := Parse(tc.content)
		if description != tc.description || body != tc.body {
			t.Fatalf("%s: got (%q, %q) want (%q, %q)", tc.name, description, body, tc.description, tc.body)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"", "Upper", "bad_name", "-bad", "bad-", "bad--name", "a/b"} {
		if ValidName(name) == nil {
			t.Fatalf("accepted invalid name: %q", name)
		}
	}
	if err := ValidName("bigquery-metrics"); err != nil {
		t.Fatal(err)
	}
}

func TestListFlagsBrokenSkills(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "alpha", "---\ndescription: First skill\n---\nBody")
	write(t, dir, "gamma", "   \n")
	if err := os.MkdirAll(filepath.Join(dir, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "Bad_Name"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	items, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %+v", items)
	}
	byName := map[string]Skill{}
	for _, item := range items {
		byName[item.Name] = item
	}
	if byName["alpha"].Description != "First skill" || byName["alpha"].Error != "" {
		t.Errorf("alpha = %+v", byName["alpha"])
	}
	if !strings.Contains(byName["beta"].Error, "missing") {
		t.Errorf("beta = %+v", byName["beta"])
	}
	if !strings.Contains(byName["gamma"].Error, "no description") {
		t.Errorf("gamma = %+v", byName["gamma"])
	}
	if !strings.Contains(byName["Bad_Name"].Error, "invalid name") {
		t.Errorf("Bad_Name = %+v", byName["Bad_Name"])
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].Name > items[i].Name {
			t.Fatalf("not sorted: %v", []string{items[i-1].Name, items[i].Name})
		}
	}
}

func TestReadStaysInsideRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, FileName), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := Read(root, "linked"); err == nil {
		t.Fatal("a symlink escaped the skills root")
	}
	if _, err := Read(root, "../outside"); err == nil {
		t.Fatal("a path escaped the skills root")
	}
}

func TestRoots(t *testing.T) {
	bot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bot, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Roots(bot, bot, "/home/u"); len(got) != 1 || got[0] != filepath.Join(bot, "skills") {
		t.Fatalf("bot roots = %v", got)
	}

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := Roots(t.TempDir(), project, "/home/u")
	if len(got) != 2 || got[0] != filepath.Join(project, ".agents", "skills") || got[1] != filepath.Join("/home/u", ".agents", "skills") {
		t.Fatalf("project roots = %v", got)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
