package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBotConfigReadsKeys(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte("model = \"gpt-4\"\nbase_url = \"http://bot\"\ntools = [\"read\", \"edit\"]\n"), 0o644)
	bc, err := loadBotConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if bc.Model != "gpt-4" || bc.BaseURL != "http://bot" {
		t.Errorf("bc = %+v", bc)
	}
	if len(bc.Tools) != 2 || bc.Tools[0] != "read" || bc.Tools[1] != "edit" {
		t.Errorf("tools = %v", bc.Tools)
	}
}

func TestLoadBotConfigMissingFile(t *testing.T) {
	root := t.TempDir()
	bc, err := loadBotConfig(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bc.Model != "" || bc.BaseURL != "" || len(bc.Tools) != 0 {
		t.Errorf("bc = %+v", bc)
	}
}

func TestLoadBotConfigMalformed(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte("model ==\n"), 0o644)
	_, err := loadBotConfig(root)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestMergeBotConfigOverrides(t *testing.T) {
	fc := fileConfig{model: "old", base: "old-base", tools: "old-tools"}
	bc := botConfig{Model: "new", BaseURL: "new-base", Tools: []string{"a", "b"}}
	merged := mergeBotConfig(fc, bc)
	if merged.model != "new" || merged.base != "new-base" || merged.tools != "a b" {
		t.Errorf("merged = %+v", merged)
	}
}

func TestMergeBotConfigEmptyKeepsFileConfig(t *testing.T) {
	fc := fileConfig{model: "keep", base: "keep-base", tools: "keep-tools"}
	merged := mergeBotConfig(fc, botConfig{})
	if merged.model != "keep" || merged.base != "keep-base" || merged.tools != "keep-tools" {
		t.Errorf("merged = %+v", merged)
	}
}

func TestResolveBotCtxNoBot(t *testing.T) {
	root := t.TempDir()
	ctx := resolveBotCtx(root)
	if ctx.IsBot {
		t.Error("IsBot should be false")
	}
	if ctx.Workspace != root {
		t.Errorf("Workspace = %q, want root", ctx.Workspace)
	}
}

func TestResolveBotCtxWithWorkspaceDir(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "workspace"), 0o755)
	ctx := resolveBotCtx(root)
	if !ctx.IsBot {
		t.Error("IsBot should be true")
	}
	if ctx.Workspace != filepath.Join(root, "workspace") {
		t.Errorf("Workspace = %q", ctx.Workspace)
	}
}

func TestResolveBotCtxCreatesWorkspace(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644)
	ctx := resolveBotCtx(root)
	want := filepath.Join(root, "workspace")
	if ctx.Workspace != want {
		t.Errorf("Workspace = %q, want %q", ctx.Workspace, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Errorf("workspace not created: %v", err)
	}
}

func TestResolveBotCtxSkillsAndPrompt(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "skills"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "bot.md"), []byte("  extra context  \n"), 0o644)
	ctx := resolveBotCtx(root)
	if ctx.Skills != filepath.Join(root, "skills") {
		t.Errorf("Skills = %q", ctx.Skills)
	}
	if ctx.Prompt != "extra context" {
		t.Errorf("Prompt = %q", ctx.Prompt)
	}
}

func TestResolveBotCtxAPIKey(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "secrets"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "secrets", "api-key"), []byte("sk-secret\n"), 0o644)
	ctx := resolveBotCtx(root)
	if !ctx.HasAPIKey || ctx.APIKey != "sk-secret" {
		t.Errorf("HasAPIKey=%v APIKey=%q", ctx.HasAPIKey, ctx.APIKey)
	}
}

func TestResolveBotCtxFileWorkspaceAssigned(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(root, "workspace"), []byte("not a dir"), 0o644)
	ctx := resolveBotCtx(root)
	if ctx.Workspace != filepath.Join(root, "workspace") {
		t.Errorf("Workspace = %q", ctx.Workspace)
	}
}

func TestConfigDirPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	t.Setenv("HOME", "/home/u")
	if got := configDir(); got != "/x/cfg" {
		t.Errorf("XDG_CONFIG_HOME wins: got %q", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	if got := configDir(); got != filepath.Join("/home/u", ".config") {
		t.Errorf("HOME fallback: got %q", got)
	}

	t.Setenv("HOME", "")
	if got := configDir(); got != "" {
		t.Errorf("nothing set: got %q", got)
	}
}

func TestAxRootFallsBackToWorkdir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)
	if got := axRoot(); got != filepath.Join(dir, ".ax") {
		t.Errorf("axRoot = %q", got)
	}
}

func TestAxRootUsesConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/settled")
	if got := axRoot(); got != filepath.Join("/settled", "ax") {
		t.Errorf("axRoot = %q", got)
	}
}

func TestWorkDirAbs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if got := workDirAbs(); got != filepath.Join(dir, ".ax") {
		t.Errorf("workDirAbs = %q", got)
	}
}

func TestPathExistsAndIsDir(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	if !pathExists(file) {
		t.Error("pathExists should be true")
	}
	if pathExists(filepath.Join(root, "missing")) {
		t.Error("pathExists should be false")
	}
	if pathIsDir(file) {
		t.Error("file is not dir")
	}
	if !pathIsDir(root) {
		t.Error("root is dir")
	}
}

func TestLoadBotConfigErrorWrapped(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "bot.toml"), 0o755)
	_, err := loadBotConfig(root)
	if err == nil || !strings.Contains(err.Error(), "read bot.toml") {
		t.Errorf("err = %v", err)
	}
}
