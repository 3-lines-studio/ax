package axrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeAX(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args.txt") + "\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func TestRunStreamsEventsAndReturnsTheAnswer(t *testing.T) {
	path, dir := fakeAX(t, `cat <<'EOF'
{"type":"protocol","version":1}
{"type":"assistant_delta","text":"Hel"}
{"type":"tool_start","id":"c1","name":"execute","target":"fsx.read"}
{"type":"tool_result","id":"c1","output":"ok"}
{"type":"usage","input":10,"output":2}
{"type":"message","message":{"Role":"assistant","Content":"tool only","ToolCalls":[{"ID":"c1"}]}}
{"type":"result","messages":[{"Role":"user","Content":"hi"},{"Role":"assistant","Content":"tool only","ToolCalls":[{"ID":"c1"}]},{"Role":"tool","Content":"ok"},{"Role":"assistant","Content":"Hello there"}],"usage":{"input":10,"output":2}}
{"type":"done","outcome":"done"}
EOF
`)
	var seen []string
	result, err := RunOne(context.Background(), Config{
		AXPath:  path,
		BotRoot: "/work",
		BaseURL: "https://api.example/v1",
		Model:   "model-a",
		Env:     []string{"AX_SLACK_CHANNEL=C1"},
	}, Job{Session: "/sessions/1.jsonl", Prompt: "hi"}, func(event Event) {
		seen = append(seen, event.Type)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Hello there" || result.Outcome != "done" {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.Input != 10 || result.Usage.Output != 2 {
		t.Errorf("usage = %+v", result.Usage)
	}
	if len(result.Messages) != 4 {
		t.Errorf("messages = %+v", result.Messages)
	}
	if len(seen) < 3 || seen[0] != "protocol" || seen[len(seen)-1] != "done" {
		t.Errorf("events = %v", seen)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--events", "--session", "/sessions/1.jsonl", "-C", "/work", "-base", "https://api.example/v1", "-model", "model-a", "hi"}
	if got := strings.Split(strings.TrimSpace(string(args)), "\n"); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestRunReportsFailureText(t *testing.T) {
	path, _ := fakeAX(t, `cat <<'EOF'
{"type":"protocol","version":1}
{"type":"error","message":"provider error: rate limit"}
{"type":"done","outcome":"failed"}
EOF
`)
	_, err := RunOne(context.Background(), Config{AXPath: path}, Job{Prompt: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelSendsControl(t *testing.T) {
	path, dir := fakeAX(t, `read line
printf '%s' "$line" > `+filepath.Join(t.TempDir(), "unused")+`
cat <<'EOF'
{"type":"done","outcome":"cancelled"}
EOF
`)
	run, err := Start(context.Background(), Config{AXPath: path}, Job{Prompt: "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := run.Cancel(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel never delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := run.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "cancelled" {
		t.Errorf("outcome = %q", result.Outcome)
	}
	_ = dir
}

func TestMissingBinaryIsAClearError(t *testing.T) {
	_, err := RunOne(context.Background(), Config{AXPath: filepath.Join(t.TempDir(), "nope")}, Job{Prompt: "hi"}, nil)
	if err == nil || !strings.Contains(err.Error(), "start ax") {
		t.Fatalf("err = %v", err)
	}
}
