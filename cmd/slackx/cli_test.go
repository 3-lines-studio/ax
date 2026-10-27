package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	binPath   string
	binErr    error
)

func buildToolHelper() (string, error) {
	buildOnce.Do(func() {
		wd, err := os.Getwd()
		if err != nil {
			binErr = err
			return
		}
		dir, err := os.MkdirTemp("", "slackx-bin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "slackx")
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("go build: %v: %s", err, out)
		}
	})
	return binPath, binErr
}

func buildTool(t *testing.T) string {
	t.Helper()
	p, err := buildToolHelper()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func envWithout(blocked ...string) []string {
	block := map[string]bool{}
	for _, b := range blocked {
		block[b] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.Index(kv, "="); i >= 0 {
			k = kv[:i]
		}
		if block[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func runToolWithEnv(t *testing.T, env []string, stdin []byte, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(buildTool(t), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = env
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func runTool(t *testing.T, stdin []byte, args ...string) (string, string, int) {
	t.Helper()
	return runToolWithEnv(t, os.Environ(), stdin, args...)
}

func TestDescribeOutput(t *testing.T) {
	stdout, stderr, code := runTool(t, nil, "describe")
	if code != 0 {
		t.Fatalf("describe exit=%d stderr=%s", code, stderr)
	}
	if strings.Count(strings.TrimSpace(stdout), "\n") != 0 {
		t.Fatalf("describe output is not a single line: %q", stdout)
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(stdout), &spec); err != nil {
		t.Fatalf("describe output is not JSON: %v", err)
	}
	if spec["name"] != "upload_to_slack" {
		t.Fatalf("name=%v", spec["name"])
	}
	params, ok := spec["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing: %v", spec)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %v", params)
	}
	if _, ok := props["path"].(map[string]any); !ok {
		t.Fatalf("path property missing: %v", props)
	}
	required, ok := params["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "path" {
		t.Fatalf("required=%v", params["required"])
	}
}

func TestDashDashIsStripped(t *testing.T) {
	stdout, stderr, code := runTool(t, nil, "--", "describe")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(stdout), &spec); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if spec["name"] != "upload_to_slack" {
		t.Fatalf("name=%v", spec["name"])
	}
}

func TestUsageForWrongArgs(t *testing.T) {
	for _, args := range [][]string{
		{"foo"},
		{"run"},
		{"run", "badtool"},
		{"run", "upload_to_slack", "extra"},
	} {
		_, stderr, code := runTool(t, nil, args...)
		if code != 2 {
			t.Fatalf("args=%v exit=%d", args, code)
		}
		if !strings.Contains(stderr, "usage:") {
			t.Fatalf("args=%v stderr=%q", args, stderr)
		}
	}
}

func TestRunRejectsInvalidJSON(t *testing.T) {
	_, stderr, code := runTool(t, []byte("{not json"), "run", "upload_to_slack")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr, "invalid arguments") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestRunRejectsMissingPath(t *testing.T) {
	_, stderr, code := runTool(t, []byte(`{}`), "run", "upload_to_slack")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.HasPrefix(stderr, "error:") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestRunRejectsNonexistentPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.png")
	input := []byte(fmt.Sprintf(`{"path":%q}`, path))
	_, stderr, code := runTool(t, input, "run", "upload_to_slack")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr, "error:") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestRunRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	input := []byte(fmt.Sprintf(`{"path":%q}`, dir))
	_, stderr, code := runTool(t, input, "run", "upload_to_slack")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr, "non-empty regular file") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestRunRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte(fmt.Sprintf(`{"path":%q}`, path))
	_, stderr, code := runTool(t, input, "run", "upload_to_slack")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr, "non-empty regular file") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestRunRequiresTokenAndChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte(fmt.Sprintf(`{"path":%q}`, path))
	env := envWithout("SLACK_BOT_TOKEN", "AX_SLACK_CHANNEL", "AX_SLACK_THREAD")
	_, stderr, code := runToolWithEnv(t, env, input, "run", "upload_to_slack")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr, "SLACK_BOT_TOKEN and AX_SLACK_CHANNEL are required") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestRunStillRequiresChannelWhenTokenPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte(fmt.Sprintf(`{"path":%q}`, path))
	env := append(envWithout("SLACK_BOT_TOKEN", "AX_SLACK_CHANNEL", "AX_SLACK_THREAD"), "SLACK_BOT_TOKEN=xoxb-test")
	_, stderr, code := runToolWithEnv(t, env, input, "run", "upload_to_slack")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr, "SLACK_BOT_TOKEN and AX_SLACK_CHANNEL are required") {
		t.Fatalf("stderr=%q", stderr)
	}
}
