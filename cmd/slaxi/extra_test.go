package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/botdir"
)

func TestSafePart(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"", false},
		{".", false},
		{"..", false},
		{"valid-id.2", true},
		{"A-z_9", true},
		{"has space", false},
		{"slash/x", false},
		{"chars*", false},
		{"tilde~", false},
	} {
		if got := safePart(tc.in); got != tc.ok {
			t.Fatalf("safePart(%q) = %v, want %v", tc.in, got, tc.ok)
		}
	}
}

func TestSplitMessageSplitsAtNewline(t *testing.T) {
	parts := splitMessage("abcdefghij\nrest", 10)
	if len(parts) != 2 || parts[0] != "abcdefghij" || parts[1] != "rest" {
		t.Fatalf("unexpected parts %#v", parts)
	}
}

func TestRunAXErrorEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"error\",\"message\":\"boom\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, stateAX: filepath.Join(dir, "sessions")}
	_, err := runAX(cfg, job{teamID: "T1", channel: "C1", threadTS: "1.1", prompt: "fix"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
}

func TestRunAXOutcomeNotDone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":\"x\"}]}' '{\"type\":\"done\",\"outcome\":\"cancelled\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, stateAX: filepath.Join(dir, "sessions")}
	_, err := runAX(cfg, job{teamID: "T1", channel: "C1", threadTS: "1.1", prompt: "fix"})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err %v", err)
	}
}

func TestRunAXEmptyResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"user\",\"Content\":\"x\"}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, stateAX: filepath.Join(dir, "sessions")}
	_, err := runAX(cfg, job{teamID: "T1", channel: "C1", threadTS: "1.1", prompt: "fix"})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("err %v", err)
	}
}

func TestRunAXToolCallSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := `#!/bin/sh
printf '%s\n' '{"type":"result","messages":[{"Role":"assistant","Content":"with tool","ToolCalls":[{"id":"1"}]},{"Role":"assistant","Content":"final"}]}' '{"type":"done","outcome":"done"}'
`
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, stateAX: filepath.Join(dir, "sessions")}
	reply, err := runAX(cfg, job{teamID: "T1", channel: "C1", threadTS: "1.1", prompt: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "final" {
		t.Fatalf("reply %q", reply)
	}
}

func TestRunAXNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	if err := os.WriteFile(ax, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, stateAX: filepath.Join(dir, "sessions")}
	_, err := runAX(cfg, job{teamID: "T1", channel: "C1", threadTS: "1.1", prompt: "fix"})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err %v", err)
	}
}

func TestLoadConfigUsesWorkingDir(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	writeBotFile(t, filepath.Join(dir, "bot.toml"), "model = \"m\"\n")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "a")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-bot-token"), "b")
	writeBotFile(t, filepath.Join(dir, "secrets", "api-key"), "k")
	t.Chdir(dir)
	t.Setenv("BOT_ROOT", "")
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.botRoot != dir {
		t.Fatalf("botRoot %q want %q", cfg.botRoot, dir)
	}
	if cfg.stateAX != filepath.Join(dir, "state", "ax", "sessions") {
		t.Fatalf("stateAX %q", cfg.stateAX)
	}
}

func TestLoadConfigBadBotToml(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	writeBotFile(t, filepath.Join(dir, "bot.toml"), "model = \"unterminated")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "a")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-bot-token"), "b")
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for malformed bot.toml")
	}
}

func TestLoadConfigMissingBotToken(t *testing.T) {
	dir := t.TempDir()
	ax := fakeAX(t, dir)
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "a")
	writeBotFile(t, filepath.Join(dir, "secrets", "api-key"), "k")
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", ax)
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "slack bot token") {
		t.Fatalf("err %v", err)
	}
}

func TestLoadConfigFindAxError(t *testing.T) {
	dir := t.TempDir()
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-app-token"), "a")
	writeBotFile(t, filepath.Join(dir, "secrets", "slack-bot-token"), "b")
	writeBotFile(t, filepath.Join(dir, "secrets", "api-key"), "k")
	t.Setenv("BOT_ROOT", dir)
	t.Setenv("SLAXI_AX_PATH", filepath.Join(dir, "does-not-exist-ax"))
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "find ax") {
		t.Fatalf("err %v", err)
	}
}

func TestReadSecretReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets", "slack-app-token"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLACK_APP_TOKEN", "")
	_, err := botdir.ReadSecret(dir, "SLACK_APP_TOKEN", "slack-app-token")
	if err == nil || !strings.Contains(err.Error(), "read secret slack-app-token") {
		t.Fatalf("err %v", err)
	}
}

func TestParseJobMalformedJSON(t *testing.T) {
	if _, ok := parseJob([]byte(`{"team_id":"T1","event":`)); ok {
		t.Fatal("malformed event accepted")
	}
}

func TestParseJobRejectsEmptyPrompt(t *testing.T) {
	data := []byte(`{"team_id":"T1","event":{"type":"app_mention","channel":"C1","text":"<@U1>","ts":"1.0"},"authorizations":[{"user_id":"U1"}]}`)
	if _, ok := parseJob(data); ok {
		t.Fatal("empty prompt accepted")
	}
}

func TestWorkPostsMultipleParts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	content := strings.Repeat("x", 2000) + "\n\n" + strings.Repeat("y", 2000)
	contentJSON, _ := json.Marshal(content)
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":" + string(contentJSON) + "}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botToken: "bot", stateAX: filepath.Join(dir, "sessions"), artifacts: filepath.Join(dir, "artifacts")}
	var calls []capturedCall
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		calls = append(calls, capturedCall{path: r.URL.Path, form: r.PostForm})
		return jsonResponse(`{"ok":true,"ts":"1.0"}`), nil
	})
	jobs := make(chan job, 1)
	jobs <- job{channel: "C1", threadTS: "1.0", prompt: "hi"}
	close(jobs)
	work(cfg, jobs)
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	if calls[0].path != "/api/chat.postMessage" || calls[0].form.Get("text") != "Working…" {
		t.Fatalf("call0 %#v", calls[0])
	}
	if calls[1].path != "/api/chat.update" || calls[1].form.Get("text") != strings.Repeat("x", 2000) {
		t.Fatalf("call1 %#v", calls[1])
	}
	if calls[2].path != "/api/chat.postMessage" || calls[2].form.Get("text") != strings.Repeat("y", 2000) {
		t.Fatalf("call2 %#v", calls[2])
	}
}

func TestWorkHandlesProgressError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":\"done\"}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botToken: "bot", stateAX: filepath.Join(dir, "sessions"), artifacts: filepath.Join(dir, "artifacts")}
	var calls []capturedCall
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		calls = append(calls, capturedCall{path: r.URL.Path, form: r.PostForm})
		if r.PostForm.Get("text") == "Working…" {
			return nil, errors.New("slack down")
		}
		return jsonResponse(`{"ok":true,"ts":"1.0"}`), nil
	})
	jobs := make(chan job, 1)
	jobs <- job{channel: "C1", threadTS: "1.0", prompt: "hi"}
	close(jobs)
	work(cfg, jobs)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].path != "/api/chat.postMessage" || calls[0].form.Get("text") != "Working…" {
		t.Fatalf("call0 %#v", calls[0])
	}
	if calls[1].path != "/api/chat.postMessage" || calls[1].form.Get("text") != "done" {
		t.Fatalf("call1 %#v", calls[1])
	}
}

func TestWorkHandlesUpdateError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":\"done\"}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botToken: "bot", stateAX: filepath.Join(dir, "sessions"), artifacts: filepath.Join(dir, "artifacts")}
	var calls []capturedCall
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		calls = append(calls, capturedCall{path: r.URL.Path, form: r.PostForm})
		if r.URL.Path == "/api/chat.update" {
			return nil, errors.New("slack down")
		}
		return jsonResponse(`{"ok":true,"ts":"1.0"}`), nil
	})
	jobs := make(chan job, 1)
	jobs <- job{channel: "C1", threadTS: "1.0", prompt: "hi"}
	close(jobs)
	work(cfg, jobs)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].path != "/api/chat.postMessage" || calls[0].form.Get("text") != "Working…" {
		t.Fatalf("call0 %#v", calls[0])
	}
	if calls[1].path != "/api/chat.update" {
		t.Fatalf("call1 %#v", calls[1])
	}
}
