package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/axproto"
)

// buildBinary builds the package at pkgPath (relative to the module root)
// into a temp file named name and returns its path. name is explicit rather
// than derived from pkgPath because filepath.Base(".") is "." -- deriving it
// would point the returned path at the temp directory itself, not a file in
// it, and exec'ing a directory fails with a misleading "permission denied".
func buildBinary(t *testing.T, pkgPath, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, pkgPath)
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building %s: %v\n%s", pkgPath, err, out)
	}
	return bin
}

// writeServersFile writes an MCPX_SERVERS_FILE entry pointing at the fake
// MCP server fixture and returns its path.
func writeServersFile(t *testing.T, serverName, fakeServerBin string) string {
	t.Helper()
	servers := []serverConfig{{Name: serverName, Command: fakeServerBin}}
	data, err := json.Marshal(servers)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "servers.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDescribe drives the real mcpx binary's `describe` command against a
// real (fixture) MCP server, and checks the tool names come back qualified
// as <server>__<tool> with their descriptions and schemas intact.
func TestDescribe(t *testing.T) {
	mcpxBin := buildBinary(t, ".", "mcpx")
	fakeServerBin := buildBinary(t, "./testdata/fake-mcp-server", "fake-mcp-server")
	serversFile := writeServersFile(t, "fake", fakeServerBin)

	cmd := exec.Command(mcpxBin, "describe")
	cmd.Env = append(os.Environ(), "MCPX_SERVERS_FILE="+serversFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("mcpx describe: %v\nstderr:\n%s", err, stderr.String())
	}

	var names []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var spec axproto.ToolSpec
		if err := json.Unmarshal([]byte(line), &spec); err != nil {
			t.Fatalf("unmarshaling tool spec line %q: %v", line, err)
		}
		names = append(names, spec.Name)
		if spec.Description == "" {
			t.Errorf("tool %q has no description", spec.Name)
		}
		if spec.Parameters == nil {
			t.Errorf("tool %q has no parameters schema", spec.Name)
		}
	}

	want := []string{"fake__echo", "fake__fail"}
	for _, w := range want {
		if !slicesContains(names, w) {
			t.Errorf("describe output missing %q; got %v", w, names)
		}
	}
}

// TestRunEcho drives `mcpx run fake__echo` end to end: real mcpx binary,
// real fixture MCP server, arguments on stdin, result on stdout.
func TestRunEcho(t *testing.T) {
	mcpxBin := buildBinary(t, ".", "mcpx")
	fakeServerBin := buildBinary(t, "./testdata/fake-mcp-server", "fake-mcp-server")
	serversFile := writeServersFile(t, "fake", fakeServerBin)

	cmd := exec.Command(mcpxBin, "run", "fake__echo")
	cmd.Env = append(os.Environ(), "MCPX_SERVERS_FILE="+serversFile)
	cmd.Stdin = strings.NewReader(`{"message":"hola alfred"}`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("mcpx run: %v\nstderr:\n%s", err, stderr.String())
	}

	got := strings.TrimSpace(stdout.String())
	want := "echo: hola alfred"
	if got != want {
		t.Errorf("mcpx run fake__echo output = %q, want %q", got, want)
	}
}

// TestRunFailedTool exercises IsError: mcpx must exit non-zero (ax's "tool
// error" convention) while still printing the tool's own message to stdout.
func TestRunFailedTool(t *testing.T) {
	mcpxBin := buildBinary(t, ".", "mcpx")
	fakeServerBin := buildBinary(t, "./testdata/fake-mcp-server", "fake-mcp-server")
	serversFile := writeServersFile(t, "fake", fakeServerBin)

	cmd := exec.Command(mcpxBin, "run", "fake__fail")
	cmd.Env = append(os.Environ(), "MCPX_SERVERS_FILE="+serversFile)
	cmd.Stdin = strings.NewReader(`{}`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected mcpx run to exit non-zero for a tool-level error")
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\nstderr:\n%s", err, err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "deliberately failed" {
		t.Errorf("stdout = %q, want the tool's error message", got)
	}
}

// TestUnconfiguredServersFile confirms an unset MCPX_SERVERS_FILE is
// treated as "nothing configured", not an error -- describe should print
// nothing and exit 0, matching how acpi only sets this env var when a
// session actually carries MCP servers.
func TestUnconfiguredServersFile(t *testing.T) {
	mcpxBin := buildBinary(t, ".", "mcpx")
	cmd := exec.Command(mcpxBin, "describe")
	cmd.Env = os.Environ() // deliberately no MCPX_SERVERS_FILE
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("mcpx describe with no servers file: %v\nstderr:\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("expected empty describe output, got %q", stdout.String())
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
