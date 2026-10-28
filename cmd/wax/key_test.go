package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate clears every source so each case starts from nothing.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	t.Setenv("BOT_ROOT", filepath.Join(home, "bot"))
	return home
}

func TestKeyFromEnvironmentWins(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "api_key = \"from-config\"\n")
	t.Setenv("DEEPSEEK_API_KEY", "from-env")
	if got := resolveDeepSeekKey(); got != "from-env" {
		t.Errorf("key = %q", got)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "openai-env")
	if got := resolveDeepSeekKey(); got != "openai-env" {
		t.Errorf("key = %q", got)
	}
}

func TestKeyFromBotSecret(t *testing.T) {
	home := isolate(t)
	root := filepath.Join(home, "bot")
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets", "api-key"), []byte("from-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveDeepSeekKey(); got != "from-secret" {
		t.Errorf("key = %q", got)
	}
}

func TestKeyFromAXConfig(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "# ax config\napi_key = \"from-config\"\nmodel = deepseek-flash\n")
	if got := resolveDeepSeekKey(); got != "from-config" {
		t.Errorf("key = %q", got)
	}
}

func TestNoKeyAnywhere(t *testing.T) {
	isolate(t)
	if got := resolveDeepSeekKey(); got != "" {
		t.Errorf("key = %q", got)
	}
}

func TestDescribeUsesTheAXKey(t *testing.T) {
	home := isolate(t)
	writeConfig(t, home, "api_key = \"from-config\"\n")
	var out bytes.Buffer
	describe(&out)
	if !strings.Contains(out.String(), "web_search") {
		t.Fatalf("web_search not advertised with the ax key:\n%s", out.String())
	}
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "config", "ax")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
