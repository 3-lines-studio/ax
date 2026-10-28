package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/botdir"
)

func TestParseMCPDocumentObject(t *testing.T) {
	document := `{
	  "mcpServers": {
	    "github": {
	      "type": "http",
	      "url": "https://api.githubcopilot.com/mcp/",
	      "headers": {"Authorization": "Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}"}
	    },
	    "local": {
	      "command": "npx",
	      "args": ["-y", "@modelcontextprotocol/server-memory"],
	      "env": {"LOG": "debug"}
	    }
	  }
	}`
	specs, _, err := parseMCPDocument([]byte(document), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %+v", specs)
	}
	if specs[0].Name != "github" || specs[0].Type != "http" || specs[0].Headers["Authorization"] == "" {
		t.Errorf("github = %+v", specs[0])
	}
	local := specs[1]
	if local.Type != "stdio" || local.Command != "npx" || len(local.Args) != 2 || local.Env["LOG"] != "debug" {
		t.Errorf("local = %+v", local)
	}
}

func TestParseMCPDocumentArray(t *testing.T) {
	document := `{"mcpServers": [
	  {"name": "linear", "url": "https://mcp.linear.app/mcp"},
	  {"name": "files", "command": "npx", "args": ["-y", "server-filesystem"]}
	]}`
	specs, _, err := parseMCPDocument([]byte(document), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Name != "linear" || specs[1].Name != "files" {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestParseMCPDocumentPathReference(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "servers.json")
	if err := os.WriteFile(nested, []byte(`{"mcpServers":{"sentry":{"url":"https://mcp.sentry.dev/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	specs, _, err := parseMCPDocument([]byte(`{"mcpServers": "./servers.json"}`), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "sentry" {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestParseMCPDocumentVariables(t *testing.T) {
	document := `{
	  "variables": {"required": ["CLIENT_ID"]},
	  "mcpServers": {"zoom": {"url": "https://mcp.zoom.us/mcp/zoom/streamable", "auth": {"CLIENT_ID": "${CLIENT_ID}", "CLIENT_SECRET": "${CLIENT_SECRET:-}"}}}
	}`
	specs, required, err := parseMCPDocument([]byte(document), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Auth == nil || specs[0].Auth.ClientID != "${CLIENT_ID}" {
		t.Fatalf("specs = %+v", specs)
	}
	if len(required) != 1 || required[0] != "CLIENT_ID" {
		t.Errorf("required = %v", required)
	}
}

func TestMCPEntriesFromSpecs(t *testing.T) {
	specs := []mcpServerSpec{
		{Name: "Zoom", Type: "http", URL: "https://mcp.zoom.us/mcp/zoom/streamable", Auth: &mcpSpecAuth{ClientID: "${CLIENT_ID}", ClientSecret: "${CLIENT_SECRET:-}"}},
		{Name: "google-docs", Type: "http", URL: "https://api.cursor.com/rest-mcp/google-docs/mcp"},
		{Name: "Playwright", Type: "stdio", Command: "npx", Args: []string{"-y", "@playwright/mcp@latest"}},
		{Name: "playwright", Type: "stdio", Command: "npx"},
	}
	entries, skipped := mcpEntriesFromSpecs(specs)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	zoom := entries[0]
	if zoom.Name != "zoom" || zoom.URL == "" {
		t.Errorf("zoom = %+v", zoom)
	}
	if zoom.ClientID != "${CLIENT_ID}" || zoom.ClientSecretEnv != "CLIENT_SECRET" {
		t.Errorf("zoom auth = %+v", zoom)
	}
	playwright := entries[1]
	if playwright.Name != "playwright" || playwright.Command != "npx" || len(playwright.Args) != 2 {
		t.Errorf("playwright = %+v", playwright)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %v", skipped)
	}
	joined := strings.Join(skipped, " ")
	if !strings.Contains(joined, "Cursor-hosted") || !strings.Contains(joined, "duplicate") {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestLoadPluginManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cursor-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
	  "name": "zoom",
	  "displayName": "Zoom",
	  "variables": {"required": ["CLIENT_ID"]},
	  "mcpServers": "./mcp.json"
	}`
	if err := os.WriteFile(filepath.Join(dir, ".cursor-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	document := `{"mcpServers":{"zoom":{"type":"http","url":"https://mcp.zoom.us/mcp/zoom/streamable","auth":{"CLIENT_ID":"${CLIENT_ID}","CLIENT_SECRET":"${CLIENT_SECRET:-}"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	imported, err := loadMCPImport(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Specs) != 1 || imported.Specs[0].Plugin != "Zoom" {
		t.Fatalf("import = %+v", imported)
	}
	if len(imported.Required) != 1 || imported.Required[0] != "CLIENT_ID" {
		t.Errorf("required = %v", imported.Required)
	}
}

func TestAppendMCPEntriesRoundTrip(t *testing.T) {
	root := t.TempDir()
	first := []mcpEntry{{
		Name:    "github",
		URL:     "https://api.githubcopilot.com/mcp/",
		Headers: map[string]string{"Authorization": "Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}"},
	}}
	second := []mcpEntry{{
		Name:            "zoom",
		URL:             "https://mcp.zoom.us/mcp/zoom/streamable",
		ClientID:        "${ZOOM_CLIENT_ID}",
		ClientSecretEnv: "ZOOM_CLIENT_SECRET",
		Scopes:          []string{"meeting:read", "meeting:write"},
	}, {
		Name:    "filesystem",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/work"},
		Env:     map[string]string{"LOG": "debug"},
		Timeout: 15,
	}}
	path := filepath.Join(root, "bot.toml")
	if err := appendMCPEntries(path, first); err != nil {
		t.Fatal(err)
	}
	if err := appendMCPEntries(path, second); err != nil {
		t.Fatal(err)
	}
	var config botConfig
	if err := botdir.LoadTOML(root, &config); err != nil {
		t.Fatalf("parse written bot.toml: %v", err)
	}
	// Servers are written as connections, not as the legacy [[mcp]] table.
	if len(config.MCP) != 0 {
		t.Fatalf("bot.toml still uses [[mcp]]: %+v", config.MCP)
	}
	if len(config.Connections) != 3 {
		t.Fatalf("entries = %+v", config.Connections)
	}
	github := config.Connections[0]
	if github.ID != "github" || github.Kind != "mcp-http" || github.Headers["Authorization"] != "Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}" {
		t.Errorf("github = %+v", github)
	}
	zoom := config.Connections[1]
	if zoom.Kind != "mcp-http" || zoom.ClientID != "${ZOOM_CLIENT_ID}" || zoom.ClientSecretEnv != "ZOOM_CLIENT_SECRET" || len(zoom.Scopes) != 2 {
		t.Errorf("zoom = %+v", zoom)
	}
	fs := config.Connections[2]
	if fs.Kind != "mcp-stdio" || fs.Command != "npx" || len(fs.Args) != 3 || fs.Env["LOG"] != "debug" || fs.Timeout != 15 {
		t.Errorf("filesystem = %+v", fs)
	}
}

func TestAddMCPEntriesRefusesDuplicates(t *testing.T) {
	root := t.TempDir()
	bot := botdir.Resolve(root)
	if err := addMCPEntries(&bot, []mcpEntry{{Name: "github", URL: "https://api.githubcopilot.com/mcp/"}}, false); err != nil {
		t.Fatal(err)
	}
	err := addMCPEntries(&bot, []mcpEntry{{Name: "github", URL: "https://api.githubcopilot.com/mcp/"}}, false)
	if err == nil || !strings.Contains(err.Error(), "already in bot.toml") {
		t.Fatalf("err = %v", err)
	}
	if err := addMCPEntries(&bot, []mcpEntry{{Name: "github", URL: "https://api.githubcopilot.com/mcp/"}}, true); err != nil {
		t.Fatalf("force: %v", err)
	}
}

func TestRegistryHasCommonServers(t *testing.T) {
	registry, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"github", "sentry", "linear", "notion", "atlassian", "stripe", "figma", "supabase", "playwright", "filesystem", "memory"} {
		entry, ok := registry.find(name)
		if !ok {
			t.Errorf("registry is missing %s", name)
			continue
		}
		if entry.URL == "" && entry.Command == "" {
			t.Errorf("%s has no target", name)
		}
	}
	github, _ := registry.find("GITHUB")
	converted := github.entry(nil)
	if converted.URL != "https://api.githubcopilot.com/mcp/" || converted.Headers["Authorization"] == "" {
		t.Errorf("github entry = %+v", converted)
	}
	if converted.Name != "github" {
		t.Errorf("name = %q", converted.Name)
	}
	cloudflare, _ := registry.find("cloudflare-docs")
	if cloudflare.entry(nil).Auth != "none" {
		t.Errorf("cloudflare auth = %q", cloudflare.entry(nil).Auth)
	}
	fs, _ := registry.find("filesystem")
	target := fs.entry([]string{"/work"})
	if len(target.Args) != 3 || target.Args[2] != "/work" {
		t.Errorf("filesystem args = %v", target.Args)
	}
}

func TestExpandEnvValueDefaults(t *testing.T) {
	t.Setenv("AX_TEST_SET", "value")
	t.Setenv("AX_TEST_EMPTY", "")
	got, err := expandEnvValue("${AX_TEST_SET}/x")
	if err != nil || got != "value/x" {
		t.Errorf("expand = %q, %v", got, err)
	}
	got, err = expandEnvValue("a${AX_TEST_MISSING:-fallback}b")
	if err != nil || got != "afallbackb" {
		t.Errorf("default expand = %q, %v", got, err)
	}
	if _, err := expandEnvValue("${AX_TEST_MISSING}"); err == nil {
		t.Error("missing variable did not fail")
	}
	if got := expandOptional("${AX_TEST_MISSING}"); got != "" {
		t.Errorf("optional = %q", got)
	}
	if got := expandOptional("${AX_TEST_EMPTY:-x}"); got != "" {
		t.Errorf("optional default = %q", got)
	}
}

func TestNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://api.githubcopilot.com/mcp/": "githubcopilot",
		"https://mcp.sentry.dev/mcp":         "sentry",
		"https://example.com/mcp":            "example",
		"http://localhost:9000/mcp":          "localhost",
	}
	for input, want := range cases {
		if got := nameFromURL(input); got != want {
			t.Errorf("nameFromURL(%q) = %q, want %q", input, got, want)
		}
	}
}
