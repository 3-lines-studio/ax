package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The mcp.json family is what Cursor, Claude Desktop and VS Code write:
//
//	{"mcpServers": {"github": {"type": "http", "url": "https://..."}}}
//
// Cursor plugins add a manifest at .cursor-plugin/plugin.json whose
// mcpServers entry is a relative path to that file.

// mcpSpecAuth is the auth block Cursor plugins use for pre-registered OAuth
// clients. Secrets are always environment references, never literals.
type mcpSpecAuth struct {
	ClientID     string   `json:"CLIENT_ID"`
	ClientSecret string   `json:"CLIENT_SECRET"`
	Scopes       []string `json:"scopes"`
}

// mcpServerSpec is one server from an mcp.json document, normalised.
type mcpServerSpec struct {
	Name    string
	Type    string
	URL     string
	Command string
	Args    []string
	Env     map[string]string
	Headers map[string]string
	Auth    *mcpSpecAuth
	Plugin  string
}

// pluginManifest is the part of .cursor-plugin/plugin.json that matters here.
type pluginManifest struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	Description string          `json:"description"`
	MCPServers  json.RawMessage `json:"mcpServers"`
	Auth        *mcpSpecAuth    `json:"auth"`
	Variables   struct {
		Required []string `json:"required"`
	} `json:"variables"`
}

// mcpImport is the result of reading one mcp.json, plugin or URL.
type mcpImport struct {
	Specs     []mcpServerSpec
	Required  []string
	Source    string
	PluginDir string
}

// loadMCPImport reads a path (file or directory), or an http(s) URL.
func loadMCPImport(target string, client *http.Client) (mcpImport, error) {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return loadMCPImportURL(target, client)
	}
	info, err := os.Stat(target)
	if err != nil {
		return mcpImport{}, err
	}
	if info.IsDir() {
		return loadMCPImportDir(target)
	}
	return loadMCPImportFile(target)
}

func loadMCPImportDir(dir string) (mcpImport, error) {
	manifestPath := filepath.Join(dir, ".cursor-plugin", "plugin.json")
	if _, err := os.Stat(manifestPath); err == nil {
		return loadPluginManifest(manifestPath)
	}
	for _, name := range []string{"mcp.json", ".mcp.json", "plugin.json"} {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return loadMCPImportFile(candidate)
		}
	}
	return mcpImport{}, fmt.Errorf("%s has no .cursor-plugin/plugin.json or mcp.json", dir)
}

func loadMCPImportFile(path string) (mcpImport, error) {
	base := filepath.Base(path)
	if base == "plugin.json" || strings.HasSuffix(filepath.ToSlash(path), "/.cursor-plugin/plugin.json") {
		return loadPluginManifest(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return mcpImport{}, err
	}
	specs, required, err := parseMCPDocument(data, filepath.Dir(path))
	if err != nil {
		return mcpImport{}, fmt.Errorf("%s: %w", path, err)
	}
	return mcpImport{Specs: specs, Required: required, Source: path, PluginDir: filepath.Dir(path)}, nil
}

func loadPluginManifest(path string) (mcpImport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return mcpImport{}, err
	}
	var manifest pluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return mcpImport{}, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(filepath.Dir(path)) // <plugin>/.cursor-plugin/plugin.json
	result := mcpImport{Source: path, PluginDir: dir, Required: manifest.Variables.Required}
	if len(manifest.MCPServers) == 0 {
		return mcpImport{}, fmt.Errorf("%s: plugin declares no mcpServers", path)
	}
	var reference string
	if err := json.Unmarshal(manifest.MCPServers, &reference); err == nil && reference != "" {
		target := reference
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, filepath.FromSlash(reference))
		}
		nested, err := loadMCPImportFile(target)
		if err != nil {
			return mcpImport{}, err
		}
		result.Specs = nested.Specs
		result.Required = append(result.Required, nested.Required...)
	} else {
		specs, required, err := parseMCPDocument(data, dir)
		if err != nil {
			return mcpImport{}, err
		}
		result.Specs = specs
		result.Required = append(result.Required, required...)
	}
	label := firstNonEmpty(manifest.DisplayName, manifest.Name)
	for i := range result.Specs {
		result.Specs[i].Plugin = label
		if result.Specs[i].Auth == nil && manifest.Auth != nil {
			result.Specs[i].Auth = manifest.Auth
		}
	}
	return result, nil
}

func loadMCPImportURL(rawURL string, client *http.Client) (mcpImport, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	fetch := func(target string) ([]byte, error) {
		resp, err := client.Get(target)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	data, err := fetch(rawURL)
	if err != nil {
		return mcpImport{}, err
	}
	base := rawURL
	if parsed, err := url.Parse(rawURL); err == nil {
		parsed.Path = filepath.ToSlash(filepath.Dir(parsed.Path))
		base = parsed.String()
	}
	specs, required, err := parseMCPDocument(data, base)
	if err != nil {
		return mcpImport{}, fmt.Errorf("%s: %w", rawURL, err)
	}
	return mcpImport{Specs: specs, Required: required, Source: rawURL}, nil
}

// parseMCPDocument accepts the shapes found in the wild: an mcpServers object,
// an array of named servers, a string pointing at another document, and the
// VS Code "servers" spelling. baseDir resolves relative references.
func parseMCPDocument(data []byte, baseDir string) ([]mcpServerSpec, []string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("not a JSON object: %w", err)
	}
	raw, ok := root["mcpServers"]
	if !ok {
		raw, ok = root["servers"]
	}
	if !ok {
		// A bare map of servers, or a single server object.
		_, isServer := root["url"]
		if _, isCommand := root["command"]; !isServer && !isCommand {
			raw = data
		}
	}
	var required []string
	if variables, ok := root["variables"]; ok {
		var schema struct {
			Required []string `json:"required"`
		}
		if json.Unmarshal(variables, &schema) == nil {
			required = schema.Required
		}
	}
	specs, err := decodeMCPServers(raw, baseDir)
	return specs, required, err
}

func decodeMCPServers(raw json.RawMessage, baseDir string) ([]mcpServerSpec, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, fmt.Errorf("no mcpServers in document")
	}
	switch trimmed[0] {
	case '{':
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &servers); err != nil {
			return nil, err
		}
		names := make([]string, 0, len(servers))
		for name := range servers {
			names = append(names, name)
		}
		sort.Strings(names)
		specs := make([]mcpServerSpec, 0, len(names))
		for _, name := range names {
			spec, err := decodeMCPServer(servers[name])
			if err != nil {
				return nil, fmt.Errorf("server %q: %w", name, err)
			}
			spec.Name = name
			specs = append(specs, spec)
		}
		return specs, nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		specs := make([]mcpServerSpec, 0, len(items))
		for index, item := range items {
			spec, err := decodeMCPServer(item)
			if err != nil {
				return nil, fmt.Errorf("server %d: %w", index+1, err)
			}
			if spec.Name == "" {
				return nil, fmt.Errorf("server %d has no name", index+1)
			}
			specs = append(specs, spec)
		}
		return specs, nil
	case '"':
		var reference string
		if err := json.Unmarshal(trimmed, &reference); err != nil {
			return nil, err
		}
		if strings.HasPrefix(reference, "http") {
			imported, err := loadMCPImportURL(reference, nil)
			if err != nil {
				return nil, err
			}
			return imported.Specs, nil
		}
		imported, err := loadMCPImportFile(filepath.Join(baseDir, filepath.FromSlash(reference)))
		if err != nil {
			return nil, err
		}
		return imported.Specs, nil
	default:
		return nil, fmt.Errorf("mcpServers must be an object, array or path")
	}
}

func decodeMCPServer(raw json.RawMessage) (mcpServerSpec, error) {
	var payload struct {
		Name    string            `json:"name"`
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Headers map[string]string `json:"headers"`
		Auth    *mcpSpecAuth      `json:"auth"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return mcpServerSpec{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(payload.Type))
	switch {
	case payload.URL != "":
		if kind == "" || kind == "sse" {
			kind = "http"
		}
	case payload.Command != "":
		kind = "stdio"
	default:
		return mcpServerSpec{}, fmt.Errorf("needs a url or a command")
	}
	return mcpServerSpec{
		Name:    payload.Name,
		Type:    kind,
		URL:     payload.URL,
		Command: payload.Command,
		Args:    payload.Args,
		Env:     payload.Env,
		Headers: payload.Headers,
		Auth:    payload.Auth,
	}, nil
}

// mcpEntriesFromSpecs converts imported servers into bot.toml entries. Skipped
// servers are returned with the reason so the CLI can explain them.
func mcpEntriesFromSpecs(specs []mcpServerSpec) (entries []mcpEntry, skipped []string) {
	used := map[string]bool{}
	for _, spec := range specs {
		name := normalizeServerName(spec.Name)
		if name == "" {
			skipped = append(skipped, fmt.Sprintf("%s: unusable name", spec.Name))
			continue
		}
		if used[name] {
			skipped = append(skipped, fmt.Sprintf("%s: duplicate name", name))
			continue
		}
		entry := mcpEntry{Name: name}
		if spec.Type == "stdio" {
			entry.Command = spec.Command
			entry.Args = append([]string(nil), spec.Args...)
			entry.Env = compactEnv(spec.Env)
			if entry.Command == "" {
				skipped = append(skipped, fmt.Sprintf("%s: no command", name))
				continue
			}
		} else {
			if strings.Contains(spec.URL, "api.cursor.com/rest-mcp") {
				skipped = append(skipped, fmt.Sprintf("%s: Cursor-hosted endpoint, not reachable outside Cursor", name))
				continue
			}
			entry.URL = spec.URL
			entry.Headers = compactEnv(spec.Headers)
			if spec.Auth != nil {
				entry.ClientID = strings.TrimSpace(spec.Auth.ClientID)
				entry.ClientSecretEnv = envName(spec.Auth.ClientSecret)
				entry.Scopes = spec.Auth.Scopes
				if entry.ClientID != "" && envName(entry.ClientID) != "" {
					entry.ClientID = "${" + envName(entry.ClientID) + "}"
				}
			}
		}
		used[name] = true
		entries = append(entries, entry)
	}
	return entries, skipped
}

// envName returns the variable a ${VAR} or ${VAR:-...} reference names.
func envName(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return ""
	}
	inner := value[2 : len(value)-1]
	name, _, _ := strings.Cut(inner, ":-")
	name = strings.TrimSpace(name)
	if !validEnvName(name) {
		return ""
	}
	return name
}

func compactEnv(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeServerName makes an imported name usable as a catalog id.
func normalizeServerName(name string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			out.WriteRune(r)
		case r == ' ' || r == '.' || r == '/':
			out.WriteByte('-')
		}
	}
	value := strings.Trim(out.String(), "-")
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

// renderMCPEntry writes one [[mcp]] table.
func renderMCPEntry(entry mcpEntry) string {
	var out strings.Builder
	out.WriteString("[[mcp]]\n")
	out.WriteString("name = " + tomlString(entry.Name) + "\n")
	if entry.Command != "" {
		out.WriteString("command = " + tomlString(entry.Command) + "\n")
	}
	if len(entry.Args) > 0 {
		out.WriteString("args = " + tomlStrings(entry.Args) + "\n")
	}
	if len(entry.Env) > 0 {
		out.WriteString("env = " + tomlInlineTable(entry.Env) + "\n")
	}
	if len(entry.PassEnv) > 0 {
		out.WriteString("pass_env = " + tomlStrings(entry.PassEnv) + "\n")
	}
	if entry.URL != "" {
		out.WriteString("url = " + tomlString(entry.URL) + "\n")
	}
	if len(entry.Headers) > 0 {
		out.WriteString("headers = " + tomlInlineTable(entry.Headers) + "\n")
	}
	if entry.TokenEnv != "" {
		out.WriteString("token_env = " + tomlString(entry.TokenEnv) + "\n")
	}
	if entry.Auth != "" {
		out.WriteString("auth = " + tomlString(entry.Auth) + "\n")
	}
	if entry.ClientID != "" {
		out.WriteString("client_id = " + tomlString(entry.ClientID) + "\n")
	}
	if entry.ClientSecretEnv != "" {
		out.WriteString("client_secret_env = " + tomlString(entry.ClientSecretEnv) + "\n")
	}
	if len(entry.Scopes) > 0 {
		out.WriteString("scopes = " + tomlStrings(entry.Scopes) + "\n")
	}
	if entry.Timeout > 0 {
		out.WriteString("timeout = " + strconv.Itoa(entry.Timeout) + "\n")
	}
	return out.String()
}

func tomlString(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&out, `\u%04X`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

func tomlStrings(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, tomlString(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func tomlInlineTable(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, tomlString(key)+" = "+tomlString(values[key]))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// appendMCPEntries adds [[mcp]] tables to bot.toml, creating the file when the
// bot does not have one yet. Existing content is never rewritten.
func appendMCPEntries(path string, entries []mcpEntry) error {
	if len(entries) == 0 {
		return nil
	}
	var block strings.Builder
	for _, entry := range entries {
		block.WriteString("\n")
		block.WriteString(renderMCPEntry(entry))
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var out bytes.Buffer
	if len(existing) == 0 {
		out.WriteString("# Bot configuration. See docs/botdir.md for every key.\n")
	} else {
		out.Write(existing)
		if !bytes.HasSuffix(existing, []byte("\n")) {
			out.WriteString("\n")
		}
	}
	out.WriteString(block.String())
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// existingMCPNames reads the [[mcp]] names already configured in a bot root.
func existingMCPNames(root string) (map[string]bool, error) {
	botConfig, err := loadBotConfig(root)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, entry := range botConfig.MCP {
		names[strings.ToLower(strings.TrimSpace(entry.Name))] = true
	}
	return names, nil
}
