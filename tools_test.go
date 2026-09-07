package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	os.WriteFile(path, []byte(content), 0o755)
	return path
}

func TestBoundedBufferTruncates(t *testing.T) {
	b := &boundedBuffer{limit: 5}
	if n, _ := b.Write([]byte("abc")); n != 3 || string(b.buf) != "abc" || b.exceeded {
		t.Errorf("first write n=%d buf=%q exceeded=%v", n, b.buf, b.exceeded)
	}
	if n, _ := b.Write([]byte("def")); n != 3 || !b.exceeded || string(b.buf) != "abc" {
		t.Errorf("overflow write n=%d buf=%q exceeded=%v", n, b.buf, b.exceeded)
	}
	if n, _ := b.Write([]byte("more")); n != 4 || string(b.buf) != "abc" {
		t.Errorf("post-exceed write n=%d buf=%q", n, b.buf)
	}
}

func TestBoundedOutputExitCode(t *testing.T) {
	path := writeMockTool(t, mockExit3)
	cmd := exec.Command(path)
	code, stdout, stderr, err := boundedOutput(cmd, 1024)
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
	code, stdout, _, err := boundedOutput(cmd, 1024)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestBoundedOutputMissingCommand(t *testing.T) {
	cmd := exec.Command("/nonexistent/tool/describe")
	_, _, _, err := boundedOutput(cmd, 1024)
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
	if got := tool.Run("world", nil); got != "hello world" {
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
	got := runExternalTool(path, "greet", "world", t.TempDir())
	if got != "hello world" {
		t.Errorf("run = %q", got)
	}
}

func TestRunExternalToolStderrOnFailure(t *testing.T) {
	path := writeMockTool(t, mockGreetTool)
	got := runExternalTool(path, "other", "x", t.TempDir())
	if !strings.Contains(got, "unknown tool other") {
		t.Errorf("run = %q", got)
	}
}

func TestRunExternalToolNoOutput(t *testing.T) {
	path := writeMockTool(t, mockEmptyRunOut)
	got := runExternalTool(path, "greet", "", t.TempDir())
	if !strings.Contains(got, "returned no output") {
		t.Errorf("run = %q", got)
	}
}

func TestRunExternalToolMissingCommand(t *testing.T) {
	got := runExternalTool("/no/such/binary", "greet", "", t.TempDir())
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
	got := runTool([]Tool{tool}, ToolCall{Name: "echo", Arguments: "a"}, func(s string) { progressSeen = append(progressSeen, s) })
	if got != "ok:a" {
		t.Errorf("run = %q", got)
	}
	if !reflect.DeepEqual(progressSeen, []string{"chunk1"}) {
		t.Errorf("progress = %v", progressSeen)
	}
}

func TestRunToolUnknown(t *testing.T) {
	if got := runTool(nil, ToolCall{Name: "nope"}, nil); got != "error: unknown tool: nope" {
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
