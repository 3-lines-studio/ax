package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/mcp"
)

func TestServeMCPExposesSearchAndExecute(t *testing.T) {
	cat := testCatalog(t)
	tools := catalogTools(cat)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"execute","arguments":{"tool":"fake.echo","arguments":{"message":"hi"}}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := serveMCP(strings.NewReader(input), &out, tools); err != nil {
		t.Fatal(err)
	}
	responses := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		id, _ := response["id"].(float64)
		responses[id] = response
	}
	if len(responses) != 4 {
		t.Fatalf("responses = %v", responses)
	}
	discover := responses[1]["result"].(map[string]any)
	if discover["supportedVersions"].([]any)[0] != mcp.LatestProtocol {
		t.Errorf("discover = %v", discover)
	}
	listed := responses[2]["result"].(map[string]any)["tools"].([]any)
	if len(listed) != 2 {
		t.Errorf("tools = %v", listed)
	}
	search := responses[3]["result"].(map[string]any)
	if !strings.Contains(search["content"].([]any)[0].(map[string]any)["text"].(string), "fake.echo") {
		t.Errorf("search result = %v", search)
	}
	execute := responses[4]["result"].(map[string]any)
	if execute["isError"] != false || !strings.Contains(execute["content"].([]any)[0].(map[string]any)["text"].(string), `echo:{"message":"hi"}`) {
		t.Errorf("execute result = %v", execute)
	}
}

func TestMCPSourceUsesCache(t *testing.T) {
	bin := buildMCPFakeServer(t)
	cacheDir := t.TempDir()
	cfg := mcp.ServerConfig{Name: "fake", Command: bin, Timeout: 10 * time.Second}
	source := newMCPSource(cfg, cacheDir, false)
	tools, err := source.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].ID == "" {
		t.Fatalf("tools = %+v", tools)
	}
	_ = source.Close()

	// With the binary gone, a warm cache must still list tools.
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	cached := newMCPSource(cfg, cacheDir, false)
	tools, err = cached.List(context.Background())
	if err != nil {
		t.Fatalf("cache miss after removing server: %v", err)
	}
	if len(tools) != 2 {
		t.Errorf("cached tools = %+v", tools)
	}
	_ = cached.Close()
}

func TestMCPSourceCall(t *testing.T) {
	bin := buildMCPFakeServer(t)
	cfg := mcp.ServerConfig{Name: "fake", Command: bin, Timeout: 10 * time.Second}
	source := newMCPSource(cfg, t.TempDir(), true)
	defer func() { _ = source.Close() }()
	result, err := source.Call(context.Background(), "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text, "echo: hi") {
		t.Errorf("result = %+v", result)
	}
	result, err = source.Call(context.Background(), "fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Text, "deliberately failed") {
		t.Errorf("fail result = %+v", result)
	}
}

func buildMCPFakeServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-server")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/3-lines-studio/ax/internal/mcp/testdata/fake-server")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake MCP server: %v\n%s", err, out)
	}
	return bin
}

func TestAxMCPClientAgainstAxMCPSServe(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ax")
	build := exec.Command("go", "build", "-o", bin, "github.com/3-lines-studio/ax/cmd/ax")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ax: %v\n%s", err, out)
	}
	dir := t.TempDir()
	provider := filepath.Join(dir, "echoer")
	if err := os.WriteFile(provider, []byte(`#!/bin/sh
if [ "$1" = "describe" ]; then
  echo '{"name":"echo","description":"Echo a message","parameters":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}}'
  exit 0
fi
if [ "$1" = "run" ]; then
  cat >/dev/null
  echo ran
  exit 0
fi
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := mcp.New(mcp.ServerConfig{
		Name:    "ax",
		Command: bin,
		Args:    []string{"mcp", "serve"},
		Dir:     dir,
		Env:     []string{"AX_TOOLS=" + provider, "XDG_CONFIG_HOME=" + t.TempDir(), "HOME=" + t.TempDir()},
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "search" || tools[1].Name != "execute" {
		t.Fatalf("tools = %+v", tools)
	}
	result, err := client.CallTool(ctx, "search", json.RawMessage(`{"query":"echo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "echoer.echo") {
		t.Errorf("search = %q", result.Text())
	}
	result, err = client.CallTool(ctx, "execute", json.RawMessage(`{"tool":"echoer.echo","arguments":{"message":"hi"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text(), "ran") {
		t.Errorf("execute = %+v", result)
	}
}
