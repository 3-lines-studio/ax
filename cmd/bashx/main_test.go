package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/botdir"
	"unicode/utf8"
)

func TestDescribeValid(t *testing.T) {
	var descriptor map[string]any
	if err := json.Unmarshal([]byte(describe), &descriptor); err != nil {
		t.Fatalf("descriptor is not valid JSON: %v", err)
	}
	if descriptor["name"] != "bash" {
		t.Errorf("name = %v, want bash", descriptor["name"])
	}
	description, _ := descriptor["description"].(string)
	if strings.TrimSpace(description) == "" {
		t.Error("description is empty")
	}
	params, ok := descriptor["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters is not an object: %T", descriptor["parameters"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties is not an object: %T", params["properties"])
	}
	if _, ok := props["command"]; !ok {
		t.Error("parameters.properties is missing command")
	}
	if _, ok := props["timeout"]; !ok {
		t.Error("parameters.properties is missing timeout")
	}
	if required, ok := params["required"].([]any); !ok || len(required) != 1 || required[0] != "command" {
		t.Errorf("required = %v, want [command]", params["required"])
	}
	if descriptor["snippet"] == "" {
		t.Error("snippet is empty")
	}
}

func TestTailBounded(t *testing.T) {
	if got := tail(strings.Repeat("x", maxOutput+100)); len(got) != maxOutput {
		t.Errorf("tail length = %d, want %d", len(got), maxOutput)
	}
	if got := tail(strings.Repeat("é", maxOutput/2)); len(got) != maxOutput {
		t.Errorf("tail of multibyte runes length = %d, want %d", len(got), maxOutput)
	}
	if got := tail("hello"); got != "hello" {
		t.Errorf("tail of short output = %q, want %q", got, "hello")
	}
	full := strings.Repeat("a", maxOutput-1) + "é" + strings.Repeat("b", 20)
	got := tail(full)
	if len(got) > maxOutput {
		t.Errorf("tail exceeded maxOutput: %d", len(got))
	}
	if strings.Contains(got, "\uFFFD") {
		t.Errorf("tail broke a multibyte rune: %q", got)
	}
}

func TestStatusString(t *testing.T) {
	if got := statusString(nil); got != "" {
		t.Errorf("statusString(nil) = %q, want empty", got)
	}
	err := exec.Command("/bin/sh", "-c", "exit 3").Run()
	if err == nil {
		t.Fatal("expected an exit error")
	}
	if got := statusString(err); got != "exit status: 3" {
		t.Errorf("statusString = %q, want \"exit status: 3\"", got)
	}
}

func TestCappedBuffer(t *testing.T) {
	var b cappedBuffer
	chunk := strings.Repeat("x", 1024)
	for range 64 {
		if _, err := b.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := len(b.String()); got != maxOutput {
		t.Errorf("cappedBuffer length = %d, want %d", got, maxOutput)
	}
}

func TestCappedBufferKeepsNewest(t *testing.T) {
	var b cappedBuffer
	if _, err := b.Write([]byte(strings.Repeat("a", maxOutput))); err != nil {
		t.Fatalf("Write prefix: %v", err)
	}
	if _, err := b.Write([]byte("bbbb")); err != nil {
		t.Fatalf("Write suffix: %v", err)
	}
	got := b.String()
	if len(got) != maxOutput {
		t.Errorf("length = %d, want %d", len(got), maxOutput)
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxOutput-4)) {
		t.Error("capped buffer dropped the oldest bytes instead of trimming from the front")
	}
	if !strings.HasSuffix(got, "bbbb") {
		t.Errorf("capped buffer dropped the newest bytes; suffix = %q", got[len(got)-4:])
	}
}

func TestAppendLine(t *testing.T) {
	cases := []struct {
		name, output, line, want string
	}{
		{"empty output", "", "line", "line"},
		{"no trailing newline", "x", "line", "x\nline"},
		{"already newline", "x\n", "line", "x\nline"},
		{"multiple newlines", "x\n\n", "line", "x\n\nline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendLine(tc.output, tc.line); got != tc.want {
				t.Errorf("appendLine(%q, %q) = %q, want %q", tc.output, tc.line, got, tc.want)
			}
		})
	}
}

func TestPathIsDir(t *testing.T) {
	dir := t.TempDir()
	if !botdir.IsDir(dir) {
		t.Errorf("botdir.IsDir(tempdir) = false, want true")
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if botdir.IsDir(file) {
		t.Errorf("botdir.IsDir(file) = true, want false")
	}
	if botdir.IsDir(filepath.Join(dir, "nope")) {
		t.Errorf("botdir.IsDir(nonexistent) = true, want false")
	}
}

func TestWorkspacePath(t *testing.T) {
	t.Run("bot root with workspace subdir", func(t *testing.T) {
		root := t.TempDir()
		ws := filepath.Join(root, "workspace")
		if err := os.Mkdir(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BOT_ROOT", root)
		got, err := workspacePath()
		if err != nil {
			t.Fatalf("workspacePath: %v", err)
		}
		want, _ := filepath.Abs(ws)
		if got != want {
			t.Errorf("workspacePath = %q, want %q", got, want)
		}
	})
	t.Run("bot root without workspace subdir", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("BOT_ROOT", root)
		got, err := workspacePath()
		if err != nil {
			t.Fatalf("workspacePath: %v", err)
		}
		want, _ := filepath.Abs(root)
		if got != want {
			t.Errorf("workspacePath = %q, want %q", got, want)
		}
	})
	t.Run("bot data root wins over bot root", func(t *testing.T) {
		root := t.TempDir()
		data := t.TempDir()
		ws := filepath.Join(data, "workspace")
		if err := os.Mkdir(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BOT_ROOT", root)
		t.Setenv("BOT_DATA", data)
		got, err := workspacePath()
		if err != nil {
			t.Fatalf("workspacePath: %v", err)
		}
		want, _ := filepath.Abs(ws)
		if got != want {
			t.Errorf("workspacePath = %q, want %q", got, want)
		}
	})
	t.Run("standalone workspace dir", func(t *testing.T) {
		t.Setenv("BOT_ROOT", "")
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "workspace"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		got, err := workspacePath()
		if err != nil {
			t.Fatalf("workspacePath: %v", err)
		}
		want, _ := filepath.Abs(filepath.Join(dir, "workspace"))
		if got != want {
			t.Errorf("workspacePath = %q, want %q", got, want)
		}
	})
	t.Run("standalone fallback to cwd", func(t *testing.T) {
		t.Setenv("BOT_ROOT", "")
		dir := t.TempDir()
		t.Chdir(dir)
		got, err := workspacePath()
		if err != nil {
			t.Fatalf("workspacePath: %v", err)
		}
		want, _ := filepath.Abs(dir)
		if got != want {
			t.Errorf("workspacePath = %q, want %q", got, want)
		}
	})
}

func TestWorkspacePathErrors(t *testing.T) {
	t.Run("missing bot root", func(t *testing.T) {
		t.Setenv("BOT_ROOT", filepath.Join(t.TempDir(), "nope"))
		if _, err := workspacePath(); err == nil {
			t.Fatal("expected an error for a nonexistent BOT_ROOT")
		}
	})
	t.Run("bot root is a file", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "f")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BOT_ROOT", file)
		_, err := workspacePath()
		if err == nil {
			t.Fatal("expected an error when BOT_ROOT is a file")
		}
		if !strings.Contains(err.Error(), "is not a directory") {
			t.Errorf("err = %q, want to contain %q", err, "is not a directory")
		}
	})
}

func TestStatusStringSignal(t *testing.T) {
	err := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", "kill -9 $$").Run()
	if err == nil {
		t.Fatal("expected an error from a self-killed command")
	}
	if got := statusString(err); got != "signal: 9" {
		t.Errorf("statusString = %q, want %q", got, "signal: 9")
	}
}

func TestStatusStringPlainError(t *testing.T) {
	if got := statusString(errors.New("boom")); got != "boom" {
		t.Errorf("statusString = %q, want %q", got, "boom")
	}
}

func TestMainDescribe(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"plain", []string{"bashx", "describe"}},
		{"double dash", []string{"bashx", "--", "describe"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldArgs := os.Args
			oldStdout := os.Stdout
			defer func() {
				os.Args = oldArgs
				os.Stdout = oldStdout
			}()
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			os.Args = tc.args
			os.Stdout = w
			main()
			_ = w.Close()
			out, _ := io.ReadAll(r)
			if string(out) != describe+"\n" {
				t.Errorf("main describe output %d bytes, want %d", len(out), len(describe)+1)
			}
		})
	}
}

func invokeRun(t *testing.T, input string) (stdout string, code int, msg string) {
	t.Helper()
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdin = inR
	os.Stdout = outW
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
	}()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		if _, err := io.WriteString(inW, input); err != nil {
			t.Logf("write stdin: %v", err)
		}
		_ = inW.Close()
	}()

	code, msg = run()
	_ = outW.Close()
	<-writeDone
	os.Stdin = oldStdin
	os.Stdout = oldStdout
	out, _ := io.ReadAll(outR)
	return string(out), code, msg
}

func TestRunSuccess(t *testing.T) {
	t.Setenv("BOT_ROOT", t.TempDir())
	out, code, msg := invokeRun(t, `{"command":"printf hello"}`)
	if code != 0 {
		t.Fatalf("code = %d, msg = %q", code, msg)
	}
	if msg != "" {
		t.Errorf("msg = %q, want empty", msg)
	}
	if out != "hello" {
		t.Errorf("stdout = %q, want %q", out, "hello")
	}
}

func TestRunExitStatus(t *testing.T) {
	t.Setenv("BOT_ROOT", t.TempDir())
	out, code, msg := invokeRun(t, `{"command":"exit 3"}`)
	if code != 0 {
		t.Fatalf("code = %d, msg = %q", code, msg)
	}
	if !strings.Contains(out, "error: exit status: 3") {
		t.Errorf("stdout = %q, want to contain %q", out, "error: exit status: 3")
	}
}

func TestRunStderrMerged(t *testing.T) {
	t.Setenv("BOT_ROOT", t.TempDir())
	out, code, _ := invokeRun(t, `{"command":"printf out; printf err >&2"}`)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if out != "out\nerr" {
		t.Errorf("stdout = %q, want %q", out, "out\nerr")
	}
}

func TestRunNoOutput(t *testing.T) {
	t.Setenv("BOT_ROOT", t.TempDir())
	out, code, _ := invokeRun(t, `{"command":"true"}`)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if out != "Command completed without output" {
		t.Errorf("stdout = %q, want %q", out, "Command completed without output")
	}
}

func TestRunTimeout(t *testing.T) {
	t.Setenv("BOT_ROOT", t.TempDir())
	out, code, _ := invokeRun(t, `{"command":"sleep 30","timeout":1}`)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "command timed out after 1 seconds") {
		t.Errorf("stdout = %q, want to contain timeout message", out)
	}
}

func TestRunLargeMultibyteOutput(t *testing.T) {
	t.Setenv("BOT_ROOT", t.TempDir())
	out, code, _ := invokeRun(t, `{"command":"printf '\\303\\251%.0s' {1..10000}"}`)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(out) != maxOutput {
		t.Errorf("stdout length = %d, want %d", len(out), maxOutput)
	}
	if !utf8.ValidString(out) {
		t.Error("stdout is not valid UTF-8")
	}
	if strings.Contains(out, "\uFFFD") {
		t.Error("tail cut into the middle of a multibyte rune")
	}
}

func TestRunWorkspaceError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_ROOT", file)
	_, code, msg := invokeRun(t, `{"command":"echo hi"}`)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(msg, "is not a directory") {
		t.Errorf("msg = %q, want to contain %q", msg, "is not a directory")
	}
}

func TestRunInvalidInput(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantCode int
		wantMsg  string
	}{
		{"not json", `{`, 2, "invalid input:"},
		{"unknown field", `{"command":"x","extra":1}`, 2, "unknown field"},
		{"empty command", `{"command":""}`, 2, "command is required"},
		{"timeout zero", `{"command":"x","timeout":0}`, 2, "timeout must be between 1 and 600"},
		{"timeout too big", `{"command":"x","timeout":601}`, 2, "timeout must be between 1 and 600"},
		{"timeout negative", `{"command":"x","timeout":-1}`, 2, "timeout must be between 1 and 600"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, code, msg := invokeRun(t, tc.input)
			if code != tc.wantCode {
				t.Errorf("code = %d, want %d", code, tc.wantCode)
			}
			if !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("msg = %q, want to contain %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestRunOversizedInput(t *testing.T) {
	input := strings.Repeat("a", maxInput+1)
	_, code, msg := invokeRun(t, input)
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if !strings.Contains(msg, "input exceeds 1 MiB") {
		t.Errorf("msg = %q, want to contain %q", msg, "input exceeds 1 MiB")
	}
}
