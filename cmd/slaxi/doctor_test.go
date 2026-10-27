package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorAllPresent(t *testing.T) {
	root := t.TempDir()
	writeBotFile(t, filepath.Join(root, "bot.toml"), "model = \"m\"\nbase_url = \"https://api.example/v1\"\ntools = [\"fsx\"]\n")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-app-token"), "app-token")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-bot-token"), "bot-token")
	writeBotFile(t, filepath.Join(root, "secrets", "api-key"), "api-key")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "ax"))
	writeExec(t, filepath.Join(bin, "fsx"))
	t.Setenv("BOT_ROOT", root)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("SLAXI_AX_PATH", "ax")
	t.Setenv("PATH", bin)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 0 {
		t.Fatalf("doctor returned %d:\n%s", code, out)
	}
	for _, w := range []string{"all required inputs present", "ax binary", "tool fsx", "slack app token", "slack bot token", "model key"} {
		if !strings.Contains(out, w) {
			t.Fatalf("output missing %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "missing") {
		t.Fatalf("unexpected missing report:\n%s", out)
	}
}

func TestDoctorMissingAxAndTools(t *testing.T) {
	root := t.TempDir()
	writeBotFile(t, filepath.Join(root, "bot.toml"), "tools = [\"fsx\", \"bashx\"]\n")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-app-token"), "app-token")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-bot-token"), "bot-token")
	writeBotFile(t, filepath.Join(root, "secrets", "api-key"), "api-key")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "ax"))
	t.Setenv("BOT_ROOT", root)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("SLAXI_AX_PATH", "ax")
	t.Setenv("PATH", bin)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 1 {
		t.Fatalf("expected 1 got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "missing tool fsx") || !strings.Contains(out, "missing tool bashx") {
		t.Fatalf("tools should be reported missing:\n%s", out)
	}
	if !strings.Contains(out, "ax binary") || strings.Contains(out, "missing ax binary") {
		t.Fatalf("ax should be present:\n%s", out)
	}
}

func TestDoctorMissingSecret(t *testing.T) {
	root := t.TempDir()
	writeBotFile(t, filepath.Join(root, "bot.toml"), "")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "ax"))
	t.Setenv("BOT_ROOT", root)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("SLAXI_AX_PATH", "ax")
	t.Setenv("PATH", bin)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 1 {
		t.Fatalf("expected 1 got %d:\n%s", code, out)
	}
	for _, w := range []string{"missing slack app token", "missing slack bot token", "missing model key"} {
		if !strings.Contains(out, w) {
			t.Fatalf("output missing %q:\n%s", w, out)
		}
	}
	if !strings.Contains(out, "ax binary") || strings.Contains(out, "missing ax binary") {
		t.Fatalf("ax should be present:\n%s", out)
	}
}

func TestDoctorBotRootMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("BOT_ROOT", missing)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 1 {
		t.Fatalf("expected 1 got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "bot root     missing") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestDoctorBotRootNotDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_ROOT", file)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 1 {
		t.Fatalf("expected 1 got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "bot root     missing") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestDoctorBotTomlError(t *testing.T) {
	root := t.TempDir()
	writeBotFile(t, filepath.Join(root, "bot.toml"), "model = \"unterminated")
	t.Setenv("BOT_ROOT", root)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 1 {
		t.Fatalf("expected 1 got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "bot.toml     error") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestDoctorBotRootCwd(t *testing.T) {
	root := t.TempDir()
	writeBotFile(t, filepath.Join(root, "bot.toml"), "tools = [\"fsx\"]\n")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-app-token"), "app-token")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-bot-token"), "bot-token")
	writeBotFile(t, filepath.Join(root, "secrets", "api-key"), "api-key")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "ax"))
	writeExec(t, filepath.Join(bin, "fsx"))
	t.Chdir(root)
	t.Setenv("BOT_ROOT", "")
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("SLAXI_AX_PATH", "ax")
	t.Setenv("PATH", bin)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 0 {
		t.Fatalf("expected 0 got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "all required inputs present") || !strings.Contains(out, "bot root     ") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestDoctorSecretError(t *testing.T) {
	root := t.TempDir()
	writeBotFile(t, filepath.Join(root, "bot.toml"), "")
	if err := os.MkdirAll(filepath.Join(root, "secrets", "slack-app-token"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "ax"))
	t.Setenv("BOT_ROOT", root)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("SLAXI_AX_PATH", "ax")
	t.Setenv("PATH", bin)
	code := 0
	out := captureStdout(t, func() { code = doctor() })
	if code != 1 {
		t.Fatalf("expected 1 got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "secret       error") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestMainDoctorDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	writeBotFile(t, filepath.Join(root, "bot.toml"), "tools = [\"fsx\"]\n")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-app-token"), "app")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-bot-token"), "bot")
	writeBotFile(t, filepath.Join(root, "secrets", "api-key"), "key")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "ax"))
	writeExec(t, filepath.Join(bin, "fsx"))
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	cmd := exec.Command("go", "run", ".", "doctor")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"BOT_ROOT="+root,
		"SLACK_APP_TOKEN=",
		"SLACK_BOT_TOKEN=",
		"OPENAI_API_KEY=",
		"SLAXI_AX_PATH=ax",
		"PATH="+path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run doctor failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "all required inputs present") {
		t.Fatalf("output:\n%s", out)
	}
}
