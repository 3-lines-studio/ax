package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/catalog"
)

func TestConnectionLocalProviderUsesItsID(t *testing.T) {
	bin := writeMockTool(t, mockGreetTool)
	dir := t.TempDir()
	content := fmt.Sprintf("[[connection]]\nid = \"greet\"\nkind = \"local\"\ncommand = %q\n", bin)
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

	tool, ok := cat.Lookup("greet.greet")
	if !ok || tool.Server != "greet" {
		t.Fatalf("tool = %+v ok=%v", tool, ok)
	}
	if _, ok := cat.Lookup("mocktool.greet"); ok {
		t.Error("connection id was not used as the source name")
	}
	result, err := cat.Call(context.Background(), "greet.greet", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "hello") {
		t.Errorf("result = %+v", result)
	}
}

func TestConnectionCapabilitiesNarrowTheMenu(t *testing.T) {
	bin := writeMockTool(t, mockTwoTools)
	fc := fileConfig{connections: []connectionEntry{{
		ID:           "pair",
		Kind:         "local",
		Command:      bin,
		Capabilities: []string{"one"},
	}}}
	sources, err := buildSources(fc, resolveBotCtx(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d", len(sources))
	}
	tools, err := sources[0].List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "one" {
		t.Fatalf("tools = %+v", tools)
	}
	if _, err := sources[0].Call(context.Background(), "two", nil); err == nil ||
		!strings.Contains(err.Error(), "does not enable") {
		t.Fatalf("call to a disabled tool: err = %v", err)
	}
	if _, err := sources[0].Call(context.Background(), "pair.one", nil); err != nil {
		t.Fatalf("qualified call: %v", err)
	}
}

func TestConnectionWithoutCapabilitiesListsEverything(t *testing.T) {
	bin := writeMockTool(t, mockTwoTools)
	fc := fileConfig{connections: []connectionEntry{{ID: "pair", Kind: "local", Command: bin}}}
	sources, err := buildSources(fc, resolveBotCtx(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := sources[0].List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestConnectionValidation(t *testing.T) {
	bin := writeMockTool(t, mockTwoTools)
	cases := []struct {
		name string
		fc   fileConfig
		want string
	}{
		{"missing id", fileConfig{connections: []connectionEntry{{Kind: "local", Command: bin}}}, "needs an id"},
		{"unknown kind", fileConfig{connections: []connectionEntry{{ID: "x", Kind: "ftp", Command: bin}}}, "unknown kind"},
		{"local without command", fileConfig{connections: []connectionEntry{{ID: "x", Kind: "local"}}}, "needs a command"},
		{"local with url", fileConfig{connections: []connectionEntry{{ID: "x", Kind: "local", Command: bin, URL: "https://e"}}}, "cannot use url"},
		{"local with args", fileConfig{connections: []connectionEntry{{ID: "x", Kind: "local", Command: bin, Args: []string{"--flag"}}}}, "take no args"},
		{"duplicate id", fileConfig{connections: []connectionEntry{
			{ID: "x", Kind: "local", Command: bin},
			{ID: "x", Kind: "local", Command: bin},
		}}, "duplicate connection id"},
		{"capability with space", fileConfig{connections: []connectionEntry{{ID: "x", Kind: "local", Command: bin, Capabilities: []string{"a b"}}}}, "capability"},
		{"mcp without endpoint", fileConfig{connections: []connectionEntry{{ID: "x", Kind: "mcp"}}}, "exactly one of command or url"},
		{"mcp collides with legacy mcp", fileConfig{
			connections: []connectionEntry{{ID: "fake", Kind: "mcp-http", URL: "https://example.com/mcp"}},
			mcp:         []mcpEntry{{Name: "fake", URL: "https://example.com/mcp"}},
		}, "collides"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSources(tc.fc, resolveBotCtx(t.TempDir()), true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConnectionIDIsLowercased(t *testing.T) {
	bin := writeMockTool(t, mockTwoTools)
	conns, err := resolveConnections(fileConfig{connections: []connectionEntry{
		{ID: "Web", Kind: "local", Command: bin},
	}}, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if conns[0].id != "web" {
		t.Fatalf("id = %q", conns[0].id)
	}
}

func TestConnectionMCPHTTPResolves(t *testing.T) {
	fc := fileConfig{connections: []connectionEntry{{
		ID:      "remote",
		Kind:    "mcp-http",
		URL:     "https://example.com/mcp",
		Headers: map[string]string{"X-Test": "1"},
	}}}
	conns, err := resolveConnections(fc, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 || conns[0].kind != connMCP || conns[0].cfg.Name != "remote" {
		t.Fatalf("conns = %+v", conns)
	}
	if conns[0].cfg.URL != "https://example.com/mcp" || conns[0].cfg.Headers["X-Test"] != "1" {
		t.Fatalf("cfg = %+v", conns[0].cfg)
	}
}

// A bot that only sets tools = [...] keeps the source name it had before
// connections existed, so catalog ids and stored sessions stay valid.
func TestLegacyToolsKeepTheirSourceName(t *testing.T) {
	bin := writeMockTool(t, mockGreetTool)
	fc := fileConfig{tools: bin}
	conns, err := resolveConnections(fc, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 || conns[0].id != "mocktool" || conns[0].kind != connLocal {
		t.Fatalf("conns = %+v", conns)
	}
	if len(conns[0].caps) != 0 {
		t.Errorf("legacy connections must not be capped: %+v", conns[0].caps)
	}
}

func TestConnectionAndLegacyToolsShareTheNamespace(t *testing.T) {
	bin := writeMockTool(t, mockGreetTool)
	fc := fileConfig{
		tools:       bin,
		connections: []connectionEntry{{ID: "mocktool", Kind: "local", Command: bin}},
	}
	conns, err := resolveConnections(fc, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, conn := range conns {
		ids = append(ids, conn.id)
	}
	if strings.Join(ids, ",") != "mocktool,mocktool-2" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestCappedSourceForwardsAuthState(t *testing.T) {
	inner := &authSource{}
	source := newCappedSource(inner, "x", []string{"x.echo"})
	status, ok := source.(catalog.AuthStatus)
	if !ok || !status.RequiresAuth() {
		t.Fatal("capped source lost the login hint")
	}
}

type authSource struct{ testSource }

func (s *authSource) RequiresAuth() bool { return true }
