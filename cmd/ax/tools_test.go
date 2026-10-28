package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const mockGreetTool = `#!/bin/sh
cmd="$1"
if [ "$cmd" = "describe" ]; then
  printf '%s\n' '{"name":"greet","description":"Greets the user","parameters":{"type":"object","properties":{}},"snippet":"greets"}'
  exit 0
fi
if [ "$cmd" = "run" ]; then
  name="$2"
  if [ "$name" = "greet" ]; then
    printf 'hello %s' "$(cat)"
    exit 0
  fi
  printf 'unknown tool %s' "$name" >&2
  exit 3
fi
exit 2
`

const mockTwoTools = `#!/bin/sh
if [ "$1" = "describe" ]; then
  printf '%s\n' '{"name":"one","description":"first","parameters":{}}'
  printf '%s\n' '{"name":"two","description":"second","parameters":{}}'
  exit 0
fi
exit 0
`

const mockDescribeErr = `#!/bin/sh
printf 'tool failed to load' >&2
exit 9
`

const mockBadName = `#!/bin/sh
if [ "$1" = "describe" ]; then
  printf '%s\n' '{"name":"bad name","description":"d","parameters":{}}'
  exit 0
fi
exit 1
`

const mockDuplicate = `#!/bin/sh
if [ "$1" = "describe" ]; then
  printf '%s\n' '{"name":"dup","description":"d","parameters":{}}'
  printf '%s\n' '{"name":"dup","description":"d","parameters":{}}'
  exit 0
fi
exit 1
`

const mockEmptyDesc = `#!/bin/sh
if [ "$1" = "describe" ]; then
  printf '%s\n' '{"name":"x","description":"","parameters":{}}'
  exit 0
fi
exit 1
`

const mockBadParams = `#!/bin/sh
if [ "$1" = "describe" ]; then
  printf '%s\n' '{"name":"x","description":"d","parameters":"not-an-object"}'
  exit 0
fi
exit 1
`

const mockNoDescribeOut = `#!/bin/sh
exit 0
`

const mockExit3 = `#!/bin/sh
echo "out-line"
echo "err-line" >&2
exit 3
`

const mockEmptyRunOut = `#!/bin/sh
exit 0
`

func writeMockTool(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mocktool")
	_ = os.WriteFile(path, []byte(content), 0o755)
	return path
}

func TestTailBufferKeepsLastBytes(t *testing.T) {
	b := &tailBuffer{limit: 5}
	if n, _ := b.Write([]byte("abc")); n != 3 || b.String() != "abc" || b.Truncated() {
		t.Errorf("first write n=%d buf=%q truncated=%v", n, b.String(), b.Truncated())
	}
	if n, _ := b.Write([]byte("def")); n != 3 || b.String() != "bcdef" || !b.Truncated() {
		t.Errorf("second write n=%d buf=%q truncated=%v", n, b.String(), b.Truncated())
	}
	if n, _ := b.Write([]byte("0123456789")); n != 10 || b.String() != "56789" || !b.Truncated() {
		t.Errorf("big write n=%d buf=%q", n, b.String())
	}
}

func TestBoundedOutputExitCode(t *testing.T) {
	path := writeMockTool(t, mockExit3)
	cmd := exec.Command(path)
	code, stdout, stderr, _, err := boundedDescribe(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 {
		t.Errorf("code = %d", code)
	}
	if string(stdout) != "out-line\n" || string(stderr) != "err-line\n" {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestBoundedOutputSuccess(t *testing.T) {
	path := writeMockTool(t, mockNoDescribeOut)
	cmd := exec.Command(path)
	code, stdout, _, _, err := boundedDescribe(cmd)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestBoundedOutputMissingCommand(t *testing.T) {
	cmd := exec.Command("/nonexistent/tool/describe")
	_, _, _, _, err := boundedDescribe(cmd)
	if err == nil {
		t.Fatal("expected start error")
	}
}

func TestTryExternalToolsSingle(t *testing.T) {
	path := writeMockTool(t, mockGreetTool)
	workdir := t.TempDir()
	tools, err := tryExternalTools(path, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("len = %d", len(tools))
	}
	tool := tools[0]
	if tool.Name != "greet" || tool.Description != "Greets the user" || tool.Snippet != "greets" {
		t.Errorf("tool = %+v", tool)
	}
	if got := runTool(context.Background(), tools, ToolCall{Name: "greet", Arguments: "{}"}, nil); got != "hello {}" {
		t.Errorf("run = %q", got)
	}
}

func TestTryExternalToolsMultiple(t *testing.T) {
	path := writeMockTool(t, mockTwoTools)
	tools, err := tryExternalTools(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("len = %d", len(tools))
	}
	if tools[0].Name != "one" || tools[1].Name != "two" {
		t.Errorf("names = %q %q", tools[0].Name, tools[1].Name)
	}
	if tools[0].Snippet != "first" || tools[1].Snippet != "second" {
		t.Errorf("snippet fallback = %q %q", tools[0].Snippet, tools[1].Snippet)
	}
}

func TestTryExternalToolsDescribeError(t *testing.T) {
	path := writeMockTool(t, mockDescribeErr)
	_, err := tryExternalTools(path, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "tool failed to load") {
		t.Errorf("err = %v", err)
	}
}

func TestTryExternalToolsInvalidName(t *testing.T) {
	path := writeMockTool(t, mockBadName)
	_, err := tryExternalTools(path, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "invalid tool name") {
		t.Errorf("err = %v", err)
	}
}

func TestTryExternalToolsDuplicate(t *testing.T) {
	path := writeMockTool(t, mockDuplicate)
	_, err := tryExternalTools(path, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		t.Errorf("err = %v", err)
	}
}

func TestTryExternalToolsEmptyDescription(t *testing.T) {
	path := writeMockTool(t, mockEmptyDesc)
	_, err := tryExternalTools(path, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "empty tool description") {
		t.Errorf("err = %v", err)
	}
}

func TestTryExternalToolsBadParams(t *testing.T) {
	path := writeMockTool(t, mockBadParams)
	_, err := tryExternalTools(path, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "invalid tool parameters") {
		t.Errorf("err = %v", err)
	}
}

func TestTryExternalToolsNoDescribeOutput(t *testing.T) {
	path := writeMockTool(t, mockNoDescribeOut)
	tools, err := tryExternalTools(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Errorf("len = %d", len(tools))
	}
}

func TestTryExternalToolsCommandNotFound(t *testing.T) {
	_, err := tryExternalTools("/no/such/command", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "discover tools from") {
		t.Errorf("err = %v", err)
	}
}

func TestRunExternalToolSuccess(t *testing.T) {
	path := writeMockTool(t, mockGreetTool)
	got := runExternalTool(context.Background(), path, "greet", "world", t.TempDir(), 5, nil)
	if got != "hello world" {
		t.Errorf("run = %q", got)
	}
}

func TestRunExternalToolStderrOnFailure(t *testing.T) {
	path := writeMockTool(t, mockGreetTool)
	got := runExternalTool(context.Background(), path, "other", "x", t.TempDir(), 5, nil)
	if !strings.Contains(got, "unknown tool other") {
		t.Errorf("run = %q", got)
	}
}

func TestRunExternalToolNoOutput(t *testing.T) {
	path := writeMockTool(t, mockEmptyRunOut)
	got := runExternalTool(context.Background(), path, "greet", "", t.TempDir(), 5, nil)
	if !strings.Contains(got, "returned no output") {
		t.Errorf("run = %q", got)
	}
}

func TestRunExternalToolMissingCommand(t *testing.T) {
	got := runExternalTool(context.Background(), "/no/such/binary", "greet", "", t.TempDir(), 1, nil)
	if !strings.HasPrefix(got, "error:") {
		t.Errorf("run = %q", got)
	}
}

func TestRunToolDispatch(t *testing.T) {
	var progressSeen []string
	tool := Tool{Name: "echo", Run: func(arguments string, progress func(string)) string {
		progress("chunk1")
		return "ok:" + arguments
	}}
	got := runTool(context.Background(), []Tool{tool}, ToolCall{Name: "echo", Arguments: "a"}, func(s string) { progressSeen = append(progressSeen, s) })
	if got != "ok:a" {
		t.Errorf("run = %q", got)
	}
	if !reflect.DeepEqual(progressSeen, []string{"chunk1"}) {
		t.Errorf("progress = %v", progressSeen)
	}
}

func TestRunToolUnknown(t *testing.T) {
	if got := runTool(context.Background(), nil, ToolCall{Name: "nope"}, nil); got != "error: unknown tool: nope" {
		t.Errorf("run = %q", got)
	}
}

func TestStatusFunctions(t *testing.T) {
	if got := statusString(nil); got != "" {
		t.Errorf("statusString(nil) = %q", got)
	}
	if got := statusCodeString(3); got != "exit status: 3" {
		t.Errorf("statusCodeString(3) = %q", got)
	}
	cmd := exec.Command("sh", "-c", "exit 3")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected exit error")
	}
	if got := statusString(err); got != "exit status: 3" {
		t.Errorf("statusString(exit3) = %q", got)
	}
}

func TestStatusStringSignal(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected signal error")
	}
	if got := statusString(err); !strings.HasPrefix(got, "signal:") {
		t.Errorf("statusString(signal) = %q", got)
	}
}

func TestSchemaValidation(t *testing.T) {
	schema, err := compileSchema(json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.validate(`{"path":"a.txt"}`); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
	if err := schema.validate(`{}`); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("missing required = %v", err)
	}
	if err := schema.validate(`{"path":1}`); err == nil || !strings.Contains(err.Error(), "expected string") {
		t.Errorf("wrong type = %v", err)
	}
	if err := schema.validate(`{"path":"a","limit":0}`); err == nil || !strings.Contains(err.Error(), "below") {
		t.Errorf("minimum = %v", err)
	}
	if err := schema.validate(`{"path":"a","extra":true}`); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Errorf("additionalProperties = %v", err)
	}
	if err := schema.validate(`not json`); err == nil {
		t.Error("invalid JSON accepted")
	}
}

func TestRunToolRejectsInvalidArguments(t *testing.T) {
	schema, err := compileSchema(json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	tool := Tool{Name: "bash", Validate: schema.validate, Run: func(arguments string, progress func(string)) string {
		called = true
		return "ran"
	}}
	got := runTool(context.Background(), []Tool{tool}, ToolCall{Name: "bash", Arguments: "{}"}, nil)
	if called {
		t.Fatal("tool ran with invalid arguments")
	}
	if !strings.Contains(got, "invalid arguments") {
		t.Errorf("run = %q", got)
	}
}

func TestRunExternalToolTimeout(t *testing.T) {
	path := writeMockTool(t, "#!/bin/sh\nsleep 5\necho done\n")
	start := time.Now()
	got := runExternalTool(context.Background(), path, "x", "{}", t.TempDir(), 1, nil)
	if !strings.Contains(got, "timed out") {
		t.Errorf("run = %q", got)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("timeout took %s", time.Since(start))
	}
}

func TestRunExternalToolCancellation(t *testing.T) {
	path := writeMockTool(t, "#!/bin/sh\nsleep 5\necho done\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	got := runExternalTool(ctx, path, "x", "{}", t.TempDir(), 10, nil)
	if !strings.Contains(got, "cancelled") {
		t.Errorf("run = %q", got)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("cancellation took %s", time.Since(start))
	}
}

func TestRunExternalToolBoundsOutput(t *testing.T) {
	path := writeMockTool(t, "#!/bin/sh\nhead -c 100000 /dev/zero | tr '\\0' x\necho END\n")
	got := runExternalTool(context.Background(), path, "x", "{}", t.TempDir(), 5, nil)
	if len(got) > maxOutput {
		t.Errorf("output length = %d, want <= %d", len(got), maxOutput)
	}
	if !strings.HasSuffix(got, "END\n") {
		t.Errorf("tail missing: %q", got[len(got)-20:])
	}
}

const mockSequentialTool = `#!/bin/sh
if [ "$1" = "describe" ]; then
  printf '%s\n' '{"name":"mutate","description":"mutates","parameters":{"type":"object"},"sequential":true,"timeout":7,"replay":"safe"}'
  exit 0
fi
if [ "$1" = "run" ]; then
  cat >/dev/null
  printf 'ok'
  exit 0
fi
exit 2
`

func TestTryExternalToolsReadsExecutionHints(t *testing.T) {
	path := writeMockTool(t, mockSequentialTool)
	tools, err := tryExternalTools(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	tool := tools[0]
	if !tool.Sequential || tool.Timeout != 7 || tool.Replay != "safe" {
		t.Errorf("tool = %+v", tool)
	}
	if got := runTool(context.Background(), tools, ToolCall{Name: "mutate", Arguments: "{}"}, nil); got != "ok" {
		t.Errorf("run = %q", got)
	}
}
