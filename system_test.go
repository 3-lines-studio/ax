package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPromptListsTools(t *testing.T) {
	tools := []Tool{
		{Name: "bash", Snippet: "run shell"},
		{Name: "read", Snippet: "read files"},
	}
	prompt := buildSystemPrompt(tools, "/ws", nil)
	for _, want := range []string{
		"You are an expert assistant operating inside ax.",
		"Available tools:",
		"- bash: run shell",
		"- read: read files",
		"Current working directory: /ws",
		"Guidelines:",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildSystemPromptNoToolsSaysNone(t *testing.T) {
	prompt := buildSystemPrompt(nil, "/ws", nil)
	if !strings.Contains(prompt, "- none") {
		t.Errorf("prompt missing '- none': %q", prompt)
	}
}

func TestBuildSystemPromptSkipsEmptySnippetTool(t *testing.T) {
	tools := []Tool{{Name: "quiet", Snippet: ""}}
	prompt := buildSystemPrompt(tools, "/ws", nil)
	if strings.Contains(prompt, "- quiet:") {
		t.Errorf("empty-snippet tool should not be listed: %q", prompt)
	}
}

func TestBuildSystemPromptAddsEditGuidance(t *testing.T) {
	tools := []Tool{{Name: "edit", Snippet: "edit files"}}
	prompt := buildSystemPrompt(tools, "/ws", nil)
	if !strings.Contains(prompt, "Use edit for precise changes") {
		t.Errorf("edit guidance missing: %q", prompt)
	}
}

func TestBuildSystemPromptAppendsBotPrompt(t *testing.T) {
	bot := &botCtx{Workspace: "/ws", Prompt: "follow project conventions"}
	prompt := buildSystemPrompt(nil, "/ws", bot)
	if !strings.Contains(prompt, "follow project conventions") {
		t.Errorf("bot prompt missing: %q", prompt)
	}
}

func TestBuildSystemPromptAppendsUserSystemPrompt(t *testing.T) {
	cfg := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg, "ax"), 0o755)
	_ = os.WriteFile(filepath.Join(cfg, "ax", "SYSTEM.md"), []byte("call me assistant"), 0o644)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", "")
	prompt := buildSystemPrompt(nil, "/ws", nil)
	if !strings.Contains(prompt, "call me assistant") {
		t.Errorf("user system prompt missing: %q", prompt)
	}
}

func TestBuildSystemPromptNoUserPrompt(t *testing.T) {
	cfg := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg, "ax"), 0o755)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", "")
	prompt := buildSystemPrompt(nil, "/ws", nil)
	if strings.Contains(prompt, "call me assistant") {
		t.Errorf("unexpected user prompt: %q", prompt)
	}
}

func TestResolveSystemOverride(t *testing.T) {
	cfg := cliConfig{system: "override"}
	bot := &botCtx{Workspace: "/ws", Prompt: "bot"}
	if got := resolveSystem(cfg, nil, bot); got != "override" {
		t.Errorf("resolveSystem = %q", got)
	}
}

func TestResolveSystemFallsBackToBuild(t *testing.T) {
	cfg := cliConfig{}
	bot := &botCtx{Workspace: "/ws", Prompt: "bot"}
	got := resolveSystem(cfg, nil, bot)
	if !strings.Contains(got, "You are an expert assistant") {
		t.Errorf("resolveSystem did not build prompt: %q", got)
	}
	if !strings.Contains(got, "bot") {
		t.Errorf("resolveSystem dropped bot prompt: %q", got)
	}
}

func TestUserSystemPromptReadsSYSTEMMd(t *testing.T) {
	cfg := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg, "ax"), 0o755)
	_ = os.WriteFile(filepath.Join(cfg, "ax", "SYSTEM.md"), []byte("  trimmed content  \n"), 0o644)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", "")
	if got := userSystemPrompt(); got != "trimmed content" {
		t.Errorf("userSystemPrompt = %q", got)
	}
}

func TestUserSystemPromptEmpty(t *testing.T) {
	cfg := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg, "ax"), 0o755)
	_ = os.WriteFile(filepath.Join(cfg, "ax", "SYSTEM.md"), []byte("   \n"), 0o644)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", "")
	if got := userSystemPrompt(); got != "" {
		t.Errorf("empty system prompt = %q", got)
	}
}

func TestUserSystemPromptMissingFile(t *testing.T) {
	cfg := t.TempDir()
	_ = os.MkdirAll(filepath.Join(cfg, "ax"), 0o755)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", "")
	if got := userSystemPrompt(); got != "" {
		t.Errorf("no file = %q", got)
	}
}
