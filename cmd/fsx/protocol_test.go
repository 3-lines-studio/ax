package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func builtBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fsx-bin-*")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "fsx")
		_, src, _, _ := runtime.Caller(0)
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		cmd.Dir = filepath.Dir(src)
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = &buildFailure{err: err, out: string(out)}
		}
	})
	if binErr != nil {
		t.Fatalf("build failed: %v", binErr)
	}
	return binPath
}

type buildFailure struct {
	err error
	out string
}

func (b *buildFailure) Error() string {
	return b.err.Error() + ": " + b.out
}

func protocolEnv(t *testing.T) (bin, root, workspace string) {
	t.Helper()
	bin = builtBinary(t)
	root = t.TempDir()
	workspace = filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	return bin, root, workspace
}

func runFS(t *testing.T, bin, root, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "BOT_ROOT="+root)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run failed: %v", err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func TestProtocolDescribe(t *testing.T) {
	bin, _, _ := protocolEnv(t)
	out, stderr, code := runFS(t, bin, "", "", "describe")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(descriptors) {
		t.Fatalf("expected %d descriptor lines, got %d", len(descriptors), len(lines))
	}
	seen := map[string]bool{}
	for _, line := range lines {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("invalid JSON line: %v: %s", err, line)
		}
		if value["name"] == nil || value["description"] == nil || value["parameters"] == nil || value["snippet"] == nil {
			t.Fatalf("descriptor missing required fields: %s", line)
		}
		seen[value["name"].(string)] = true
	}
	for _, d := range descriptors {
		var value map[string]any
		if err := json.Unmarshal([]byte(d), &value); err != nil {
			t.Fatal(err)
		}
		if !seen[value["name"].(string)] {
			t.Fatalf("descriptor name %q missing from describe output", value["name"])
		}
	}
}

func TestProtocolDescribeDashDash(t *testing.T) {
	bin, _, _ := protocolEnv(t)
	out, _, code := runFS(t, bin, "", "", "--", "describe")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if n := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); n != len(descriptors) {
		t.Fatalf("expected %d descriptor lines, got %d", len(descriptors), n)
	}
}

func TestProtocolUsage(t *testing.T) {
	bin, _, _ := protocolEnv(t)
	for _, args := range [][]string{
		{},
		{"run"},
		{"run", "read", "extra"},
		{"nonsense"},
		{"--"},
	} {
		_, stderr, code := runFS(t, bin, "", "", args...)
		if code != 2 || !strings.Contains(stderr, "usage:") {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, stderr)
		}
	}
}

func TestProtocolUnknownTool(t *testing.T) {
	bin, root, _ := protocolEnv(t)
	_, stderr, code := runFS(t, bin, root, `{}`, "run", "nibble")
	if code != 2 || !strings.Contains(stderr, "unknown tool: nibble") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestProtocolInputTooLarge(t *testing.T) {
	bin, root, _ := protocolEnv(t)
	big := strings.Repeat("a", maxInput+1)
	_, stderr, code := runFS(t, bin, root, big, "run", "read")
	if code != 2 || !strings.Contains(stderr, "input exceeds") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestProtocolDecodeErrors(t *testing.T) {
	bin, root, _ := protocolEnv(t)
	for _, c := range []struct{ name, stdin, want string }{
		{"empty", "", "invalid input"},
		{"trailing object", `{"path":"x"}{"path":"y"}`, "expected one JSON object"},
		{"unknown field", `{"pat":"x"}`, "unknown field"},
		{"not json", `{`, "invalid input"},
	} {
		_, stderr, code := runFS(t, bin, root, c.stdin, "run", "read")
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Fatalf("%s: code=%d stderr=%q want %q", c.name, code, stderr, c.want)
		}
	}
}

func TestProtocolReadMissingFile(t *testing.T) {
	bin, root, _ := protocolEnv(t)
	_, stderr, code := runFS(t, bin, root, `{"path":"nope.txt"}`, "run", "read")
	if code != 1 || !strings.Contains(stderr, "no such file") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestProtocolRun(t *testing.T) {
	bin, root, ws := protocolEnv(t)
	seed := filepath.Join(ws, "data.txt")
	if err := os.WriteFile(seed, []byte("one\ntwo\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}

	out, stderr, code := runFS(t, bin, root, `{"path":"data.txt","offset":2,"limit":1}`, "run", "read")
	if code != 0 {
		t.Fatalf("read page: code=%d stderr=%q", code, stderr)
	}
	if want := "two\n\n[1 more lines in file. Use offset=3 to continue.]"; out != want {
		t.Fatalf("read page out=%q want %q", out, want)
	}

	out, stderr, code = runFS(t, bin, root, `{"path":`+strconvQuote(seed)+`}`, "run", "read")
	if code != 0 {
		t.Fatalf("absolute inside: code=%d stderr=%q", code, stderr)
	}
	if out != "one\ntwo\nthree" {
		t.Fatalf("absolute inside out=%q", out)
	}

	out, stderr, code = runFS(t, bin, root, `{"path":"out.txt","content":"hello"}`, "run", "write")
	if code != 0 {
		t.Fatalf("write: code=%d stderr=%q", code, stderr)
	}
	if out != "wrote out.txt (5 bytes)" {
		t.Fatalf("write out=%q", out)
	}
	b, err := os.ReadFile(filepath.Join(ws, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Fatalf("write content=%q", b)
	}

	editPath := filepath.Join(ws, "edit.txt")
	if err := os.WriteFile(editPath, []byte("alpha beta"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runFS(t, bin, root, `{"path":"edit.txt","edits":[{"oldText":"beta","newText":"gamma"}]}`, "run", "edit")
	if code != 0 {
		t.Fatalf("edit: code=%d stderr=%q", code, stderr)
	}
	if out != "edited edit.txt (1 replacements)" {
		t.Fatalf("edit out=%q", out)
	}
	b, err = os.ReadFile(editPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "alpha gamma" {
		t.Fatalf("edit content=%q", b)
	}

	_, stderr, code = runFS(t, bin, root, `{"path":"sub","content":"x","offset":1}`, "run", "write")
	if code != 2 || !strings.Contains(stderr, "unknown field") {
		t.Fatalf("unknown write field: code=%d stderr=%q", code, stderr)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	_, stderr, code = runFS(t, bin, root, `{"path":`+strconvQuote(outside)+`}`, "run", "read")
	if code != 1 || !strings.Contains(stderr, "outside the workspace") {
		t.Fatalf("outside: code=%d stderr=%q", code, stderr)
	}

	_, stderr, code = runFS(t, bin, root, `{"path":"../escape"}`, "run", "read")
	if code != 1 || !strings.Contains(stderr, "escapes from parent") {
		t.Fatalf("dotdot escape: code=%d stderr=%q", code, stderr)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
