package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/3-lines-studio/ax/internal/botdir"
)

func TestParseJobUsesThread(t *testing.T) {
	data := []byte(`{"team_id":"T1","event":{"type":"app_mention","channel":"C1","text":"<@U1> fix it","ts":"1.1","thread_ts":"1.0"},"authorizations":[{"user_id":"U1"}]}`)
	job, ok := parseJob(data)
	if !ok {
		t.Fatal("event was rejected")
	}
	if job.teamID != "T1" || job.channel != "C1" || job.threadTS != "1.0" || job.prompt != "fix it" {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func TestParseJobStartsThread(t *testing.T) {
	data := []byte(`{"team_id":"T1","event":{"type":"app_mention","channel":"C1","text":"hello","ts":"1.1"}}`)
	job, ok := parseJob(data)
	if !ok || job.threadTS != "1.1" {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func TestParseJobAcceptsDirectMessage(t *testing.T) {
	data := []byte(`{"team_id":"T1","event":{"type":"message","channel":"D1","channel_type":"im","text":"hello","ts":"1.1"}}`)
	job, ok := parseJob(data)
	if !ok || job.channel != "D1" || job.threadTS != "1.1" || job.prompt != "hello" {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func TestParseJobRejectsBotMessage(t *testing.T) {
	data := []byte(`{"team_id":"T1","event":{"type":"message","channel":"D1","channel_type":"im","text":"hello","ts":"1.1","bot_id":"B1"}}`)
	if _, ok := parseJob(data); ok {
		t.Fatal("bot message was accepted")
	}
}

func TestParseJobRejectsUnsafePath(t *testing.T) {
	data := []byte(`{"team_id":"..","event":{"type":"app_mention","channel":"C1","text":"hello","ts":"1.1"}}`)
	if _, ok := parseJob(data); ok {
		t.Fatal("unsafe event was accepted")
	}
}

func TestFormatMarkdown(t *testing.T) {
	input := "# Results\n\n| Name | Total |\n| --- | ---: |\n| Alpha | 12 |\n| Longer | 3 |\n\n**Done**: [details](https://example.com)"
	want := "*Results*\n\n```\n| Name   | Total |\n|--------|-------|\n| Alpha  | 12    |\n| Longer | 3     |\n```\n\n*Done*: <https://example.com|details>"
	if got := formatMarkdown(input); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatMarkdownPreservesCode(t *testing.T) {
	input := "```markdown\n# title\n**bold**\n```"
	if got := formatMarkdown(input); got != input {
		t.Fatalf("got %q, want %q", got, input)
	}
}

func TestSplitMessageUsesParagraphs(t *testing.T) {
	parts := splitMessage("first paragraph\n\nsecond paragraph", 20)
	if len(parts) != 2 || parts[0] != "first paragraph" || parts[1] != "second paragraph" {
		t.Fatalf("unexpected parts: %#v", parts)
	}
}

func TestSplitMessageHandlesUnicode(t *testing.T) {
	parts := splitMessage("😀😀😀😀😀", 3)
	if len(parts) != 2 || parts[0] != "😀😀😀" || parts[1] != "😀😀" {
		t.Fatalf("unexpected parts: %#v", parts)
	}
}

func TestRunAXUsesConfiguredAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	args := filepath.Join(dir, "args")
	context := filepath.Join(dir, "context")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + args + "\nprintf '%s/%s' \"$AX_SLACK_CHANNEL\" \"$AX_SLACK_THREAD\" > " + context + "\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":\"done\"}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botRoot: "/work", stateAX: filepath.Join(dir, "sessions"), artifacts: filepath.Join(dir, "artifacts"), baseURL: "https://api.example/v1", model: "model-a", system: "be direct"}
	reply, err := runAX(cfg, job{teamID: "T1", channel: "C1", threadTS: "1.1", prompt: "fix it"})
	if err != nil || reply != "done" {
		t.Fatalf("reply %q: %v", reply, err)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	want := "--events --session " + filepath.Join(dir, "sessions", "T1", "C1", "1.1.jsonl") + " -C /work -base https://api.example/v1 -model model-a -system be direct fix it"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got, err = os.ReadFile(context)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "C1/1.1" {
		t.Fatalf("unexpected context: %q", got)
	}
}

func writeBotFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeAX(t *testing.T, dir string) string {
	t.Helper()
	ax := filepath.Join(dir, "ax")
	writeBotFile(t, ax, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(ax, 0o755); err != nil {
		t.Fatal(err)
	}
	return ax
}

func TestLoadConfigBotdir(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	writeBotFile(t, filepath.Join(dir, "bot.toml"), `model = "deepseek-v4-flash"
base_url = "https://api.deepseek.com"
tools = ["fsx", "bashx"]

[slack]
mention_only = false
`)
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "app-token")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-bot-token"), "bot-token")
	writeBotFile(t, filepath.Join(dir, "secrets", "api-key"), "api-key")
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.botRoot != dir {
		t.Fatalf("botRoot %q", cfg.botRoot)
	}
	if cfg.appToken != "app-token" || cfg.botToken != "bot-token" {
		t.Fatalf("tokens %q %q", cfg.appToken, cfg.botToken)
	}
	if cfg.model != "deepseek-v4-flash" || cfg.baseURL != "https://api.deepseek.com" {
		t.Fatalf("model %q baseURL %q", cfg.model, cfg.baseURL)
	}
	if cfg.mentionOnly {
		t.Fatal("mention_only should be false")
	}
	if cfg.stateAX != filepath.Join(dir, "state", "ax", "sessions") {
		t.Fatalf("stateAX %q", cfg.stateAX)
	}
	if cfg.workspace != filepath.Join(dir, "workspace") {
		t.Fatalf("workspace %q", cfg.workspace)
	}
	if !botdir.Exists(filepath.Join(dir, "state", "ax", "sessions")) {
		t.Fatal("state/ax/sessions not created")
	}
}

func TestLoadConfigBotDataSplit(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	ax := fakeAX(t, root)
	writeBotFile(t, filepath.Join(root, "bot.toml"), "model = \"m\"\n")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-app-token"), "app-token")
	writeBotFile(t, filepath.Join(root, "secrets", "slack-bot-token"), "bot-token")
	writeBotFile(t, filepath.Join(root, "secrets", "api-key"), "api-key")
	t.Setenv("BOT_ROOT", root)
	t.Setenv("BOT_DATA", data)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.botRoot != root {
		t.Fatalf("botRoot %q, want %q", cfg.botRoot, root)
	}
	wantWorkspace := filepath.Join(data, "workspace")
	if cfg.workspace != wantWorkspace {
		t.Fatalf("workspace %q, want %q", cfg.workspace, wantWorkspace)
	}
	if cfg.stateAX != filepath.Join(data, "state", "ax", "sessions") {
		t.Fatalf("stateAX %q", cfg.stateAX)
	}
	if cfg.runSlack != filepath.Join(data, "run", "slack") {
		t.Fatalf("runSlack %q", cfg.runSlack)
	}
	if cfg.artifacts != filepath.Join(data, "workspace", "slack", "artifacts") {
		t.Fatalf("artifacts %q", cfg.artifacts)
	}
	if info, err := os.Stat(wantWorkspace); err != nil || !info.IsDir() {
		t.Fatalf("runtime dirs must be created under BOT_DATA: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "workspace")); err == nil {
		t.Fatal("workspace must not be created on the bot root when BOT_DATA is set")
	}
}

func TestLoadConfigBotdirEnvBeatsFiles(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "env-app")
	t.Setenv("SLACK_BOT_TOKEN", "env-bot")
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("SLAXI_MODEL", "env-model")
	t.Setenv("SLAXI_BASE_URL", "https://env.example/v1")
	writeBotFile(t, filepath.Join(dir, "bot.toml"), "model = \"bot-model\"\nbase_url = \"https://bot.example/v1\"\n")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "file-app")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-bot-token"), "file-bot")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.appToken != "env-app" || cfg.botToken != "env-bot" {
		t.Fatalf("env should beat files: %q %q", cfg.appToken, cfg.botToken)
	}
	if cfg.model != "env-model" || cfg.baseURL != "https://env.example/v1" {
		t.Fatalf("env should beat bot.toml: %q %q", cfg.model, cfg.baseURL)
	}
}

func TestLoadConfigMentionOnly(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	writeBotFile(t, filepath.Join(dir, "bot.toml"), "model = \"m\"\n[slack]\nmention_only = true\n")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "a")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-bot-token"), "b")
	writeBotFile(t, filepath.Join(dir, "secrets", "api-key"), "k")
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.mentionOnly {
		t.Fatal("mention_only should be true")
	}
}

func TestLoadConfigMissingSlackSecret(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error when slack app token is absent")
	}
}

func TestLoadConfigMissingModelKey(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "a")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-bot-token"), "b")
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error when model api key is absent")
	}
}

func TestRunAXUsesThreadSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	args := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + args + "\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":\"done\"}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botRoot: "/work", stateAX: filepath.Join(dir, "sessions")}
	reply, err := runAX(cfg, job{teamID: "T1", channel: "C1", threadTS: "1.1", prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Fatalf("unexpected reply: %q", reply)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	want := "--events --session " + filepath.Join(dir, "sessions", "T1", "C1", "1.1.jsonl") + " -C /work fix it"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
