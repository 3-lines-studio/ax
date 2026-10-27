package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/catalog"
)

type testSource struct {
	name  string
	tools []catalog.Tool
}

func (s *testSource) Name() string        { return s.name }
func (s *testSource) Description() string { return "test source" }
func (s *testSource) Close() error        { return nil }
func (s *testSource) List(context.Context) ([]catalog.Tool, error) {
	return s.tools, nil
}
func (s *testSource) Call(_ context.Context, name string, args json.RawMessage) (catalog.Result, error) {
	return catalog.Result{Text: name + ":" + string(args)}, nil
}

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	source := &testSource{name: "fake", tools: []catalog.Tool{{
		Name:        "echo",
		Description: "Echo a message",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`),
		Annotations: catalog.Annotations{ReadOnly: true},
	}}}
	cat := catalog.New([]catalog.Source{source})
	cat.Load(context.Background())
	return cat
}

func TestCatalogToolsSearchAndExecute(t *testing.T) {
	cat := testCatalog(t)
	tools := catalogTools(cat)

	out := runTool(context.Background(), tools, ToolCall{Name: "search", Arguments: `{}`}, nil)
	if !strings.Contains(out, "# Catalog") || !strings.Contains(out, "fake.echo") {
		t.Fatalf("search menu = %s", out)
	}
	out = runTool(context.Background(), tools, ToolCall{Name: "search", Arguments: `{"query":"echo"}`}, nil)
	if !strings.Contains(out, "fake.echo") || !strings.Contains(out, "execute(") {
		t.Fatalf("search result = %s", out)
	}
	out = runTool(context.Background(), tools, ToolCall{Name: "execute", Arguments: `{"tool":"fake.echo","arguments":{"message":"hi"}}`}, nil)
	if out != `echo:{"message":"hi"}` {
		t.Fatalf("execute = %q", out)
	}
	out = runTool(context.Background(), tools, ToolCall{Name: "execute", Arguments: `{"tool":"fake.echo","arguments":{"other":1}}`}, nil)
	if !strings.Contains(out, "invalid arguments") {
		t.Fatalf("invalid args = %q", out)
	}
	out = runTool(context.Background(), tools, ToolCall{Name: "execute", Arguments: `{"tool":"fake.echoo","arguments":{}}`}, nil)
	if !strings.Contains(out, "unknown tool") || !strings.Contains(out, "fake.echo") {
		t.Fatalf("unknown tool = %q", out)
	}
}

func TestCatalogSystemPrompt(t *testing.T) {
	cat := testCatalog(t)
	tools := catalogTools(cat)
	prompt := buildCatalogSystemPrompt(tools, "/work", nil, cat, []string{"fake.echo"})
	for _, want := range []string{"two tools", "search", "execute", "fake (1)", "`fake.echo`", "Current working directory: /work"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestResolveMCPEntries(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("LINEAR_TOKEN", "linear-secret")
	configs, err := resolveMCPEntries([]mcpEntry{
		{Name: "GitHub", Command: "npx", Args: []string{"-y", "server"}, PassEnv: []string{"GH_TOKEN"}, Env: map[string]string{"LOG": "debug"}, Timeout: 12},
		{Name: "linear", URL: "https://mcp.linear.app/mcp", TokenEnv: "LINEAR_TOKEN"},
	}, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 {
		t.Fatalf("configs = %+v", configs)
	}
	github := configs[0]
	if github.Name != "github" || github.Dir != "/work" || github.Timeout != 12*time.Second {
		t.Errorf("github = %+v", github)
	}
	env := strings.Join(github.Env, ",")
	if !strings.Contains(env, "GH_TOKEN=secret") || !strings.Contains(env, "LOG=debug") {
		t.Errorf("env = %v", github.Env)
	}
	linear := configs[1]
	if linear.Headers["Authorization"] != "Bearer linear-secret" {
		t.Errorf("headers = %v", linear.Headers)
	}
}

func TestResolveMCPEntriesRejectsBadConfig(t *testing.T) {
	t.Setenv("LINEAR_TOKEN", "")
	cases := [][]mcpEntry{
		{{Name: "both", Command: "x", URL: "https://x"}},
		{{Name: "Bad Name", Command: "x"}},
		{{Name: "dup", Command: "x"}, {Name: "dup", Command: "y"}},
		{{Name: "linear", URL: "https://x", TokenEnv: "LINEAR_TOKEN"}},
	}
	for _, entries := range cases {
		if _, err := resolveMCPEntries(entries, "/w"); err == nil {
			t.Errorf("resolveMCPEntries(%+v) succeeded", entries)
		}
	}
}

func TestLocalSourceName(t *testing.T) {
	used := map[string]bool{}
	if got := localSourceName("/usr/local/bin/fsx", used); got != "fsx" {
		t.Errorf("fsx = %q", got)
	}
	if got := localSourceName("./bash_provider", used); got != "bash_provider" {
		t.Errorf("bash = %q", got)
	}
	if got := localSourceName("/opt/my provider", used); got != "my_provider" {
		t.Errorf("sanitize = %q", got)
	}
	if got := localSourceName("/usr/bin/fsx", used); got != "fsx-2" {
		t.Errorf("dedupe = %q", got)
	}
}

func TestLoadBotConfigMCP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/bot.toml", `
tool_mode = "catalog"
core_tools = ["fsx.read"]

[[mcp]]
name = "github"
command = "npx"
args = ["-y", "server-github"]
pass_env = ["GITHUB_TOKEN"]
`)
	bc, err := loadBotConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bc.ToolMode != "catalog" || len(bc.CoreTools) != 1 || len(bc.MCP) != 1 || bc.MCP[0].Name != "github" {
		t.Fatalf("bot config = %+v", bc)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSourcesFromBotTomlMCP(t *testing.T) {
	bin := buildMCPFakeServer(t)
	dir := t.TempDir()
	content := fmt.Sprintf("[[mcp]]\nname = \"fake\"\ncommand = %q\ntimeout = 10\n", bin)
	if err := os.WriteFile(filepath.Join(dir, "bot.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	bc, err := loadBotConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	fc := mergeBotConfig(fileConfig{}, bc)
	sources, err := buildSources(fc, resolveBotCtx(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(sources)
	cat.Load(context.Background())
	defer func() { _ = cat.Close() }()
	tool, ok := cat.Lookup("fake.echo")
	if !ok || tool.Server != "fake" {
		t.Fatalf("tool = %+v ok=%v", tool, ok)
	}
	result, err := cat.Call(context.Background(), "fake.echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "echo: hi") {
		t.Errorf("result = %+v", result)
	}
}

func TestExecuteToolStartCarriesTarget(t *testing.T) {
	sink := &eventSink{calls: map[string]ToolCall{}}
	out := captureStdout(t, func() {
		sink.toolStart(ToolCall{ID: "c1", Name: "execute", Arguments: `{"tool":"fsx.read","arguments":{}}`})
	})
	lines := decodeLines(t, out)
	if len(lines) != 1 || lines[0]["target"] != "fsx.read" {
		t.Fatalf("event = %v", lines)
	}
	out = captureStdout(t, func() {
		sink.toolStart(ToolCall{ID: "c2", Name: "bash", Arguments: `{}`})
	})
	lines = decodeLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("event = %v", lines)
	}
	if _, ok := lines[0]["target"]; ok {
		t.Errorf("direct event has target: %v", lines[0])
	}
}
