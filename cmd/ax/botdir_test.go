package main

import (
	"path/filepath"
	"testing"
)

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

func TestBotRootArgSkipsValueFlags(t *testing.T) {
	args := []string{"-tool-mode", "direct", "-thinking", "off", "-image", "x.png", "-C", "/bot", "prompt"}
	if got := botRootArg(args); got != "/bot" {
		t.Errorf("botRootArg = %q", got)
	}
	if got := botRootArg([]string{"-C=/other", "prompt"}); got != "/other" {
		t.Errorf("inline -C = %q", got)
	}
}
