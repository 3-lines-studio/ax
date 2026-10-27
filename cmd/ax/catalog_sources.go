package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/3-lines-studio/ax/internal/catalog"
	"github.com/3-lines-studio/ax/internal/mcp"
)

// localSource exposes one describe/run provider as a catalog source. The
// provider is discovered lazily so a broken binary shows up as an unavailable
// server in the menu instead of killing every other integration.
type localSource struct {
	command string
	name    string
	workdir string

	mu     sync.Mutex
	tools  []Tool
	listed bool
}

func newLocalSource(command, name, workdir string) *localSource {
	return &localSource{command: command, name: name, workdir: workdir}
}

func (s *localSource) Name() string { return s.name }

func (s *localSource) Description() string {
	return "Local provider (" + filepath.Base(s.command) + ")"
}

func (s *localSource) List(ctx context.Context) ([]catalog.Tool, error) {
	tools, err := s.discover(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]catalog.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, catalog.Tool{
			ID:          s.name + "." + tool.Name,
			Server:      s.name,
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: append([]byte(nil), tool.Parameters...),
			Annotations: catalog.Annotations{
				ReadOnly:    tool.ReadOnly,
				Destructive: tool.Destructive,
				OpenWorld:   tool.OpenWorld,
				Sequential:  tool.Sequential,
				Idempotent:  tool.Replay == "safe" || tool.ReadOnly,
			},
		})
	}
	return out, nil
}

func (s *localSource) discover(ctx context.Context) ([]Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listed {
		return s.tools, nil
	}
	tools, err := discoverProvider(ctx, s.command, s.workdir)
	if err != nil {
		return nil, err
	}
	s.tools = tools
	s.listed = true
	return tools, nil
}

func (s *localSource) Call(ctx context.Context, name string, args json.RawMessage) (catalog.Result, error) {
	tools, err := s.discover(ctx)
	if err != nil {
		return catalog.Result{}, err
	}
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		output := runTool(ctx, []Tool{tool}, ToolCall{Name: name, Arguments: string(args)}, nil)
		return catalog.Result{
			Text:    output,
			IsError: strings.HasPrefix(output, "error:"),
		}, nil
	}
	return catalog.Result{}, fmt.Errorf("unknown tool %q on %s", name, s.name)
}

func (s *localSource) Close() error { return nil }

// mcpSource exposes one MCP server as a catalog source. The tool list is
// cached on disk; search with a warm cache does not spawn the server.
type mcpSource struct {
	cfg         mcp.ServerConfig
	cacheDir    string
	fingerprint string
	refresh     bool

	mu           sync.Mutex
	client       *mcp.Client
	tools        []mcp.Tool
	instructions string
	stale        bool
}

type mcpCache struct {
	Version      int        `json:"version"`
	Fingerprint  string     `json:"fingerprint"`
	FetchedAt    int64      `json:"fetched_at"` // unix milliseconds
	TTLMS        int64      `json:"ttl_ms"`
	Instructions string     `json:"instructions,omitempty"`
	Tools        []mcp.Tool `json:"tools"`
}

const (
	mcpCacheVersion = 1
	mcpDefaultTTL   = 10 * 60 * 1000 // milliseconds
)

func newMCPSource(cfg mcp.ServerConfig, cacheDir string, refresh bool) *mcpSource {
	return &mcpSource{
		cfg:         cfg,
		cacheDir:    cacheDir,
		fingerprint: configFingerprint(cfg),
		refresh:     refresh,
	}
}

func (s *mcpSource) Name() string { return s.cfg.Name }

func (s *mcpSource) Description() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if text := strings.TrimSpace(s.instructions); text != "" {
		return oneLineText(text, 120)
	}
	if s.stale {
		return "MCP server (cached)"
	}
	return "MCP server"
}

func (s *mcpSource) List(ctx context.Context) ([]catalog.Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.refresh {
		if cached, ok := s.readCache(); ok {
			s.tools = cached.Tools
			s.instructions = cached.Instructions
			return s.mapped(), nil
		}
	}
	client, err := mcp.New(s.cfg)
	if err != nil {
		return nil, err
	}
	if err := client.Connect(ctx); err != nil {
		if cached, ok := s.readCache(); ok {
			s.tools = cached.Tools
			s.instructions = cached.Instructions
			s.stale = true
			return s.mapped(), nil
		}
		return nil, err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		if cached, ok := s.readCache(); ok {
			s.tools = cached.Tools
			s.instructions = cached.Instructions
			s.stale = true
			return s.mapped(), nil
		}
		return nil, err
	}
	s.client = client
	s.tools = tools
	s.instructions = client.Instructions()
	s.writeCache(client.TTL())
	return s.mapped(), nil
}

func (s *mcpSource) mapped() []catalog.Tool {
	out := make([]catalog.Tool, 0, len(s.tools))
	for _, tool := range s.tools {
		annotations := catalog.Annotations{Sequential: true}
		if tool.Annotations != nil {
			annotations.ReadOnly = tool.Annotations.ReadOnlyHint
			annotations.Destructive = tool.Annotations.DestructiveHint
			annotations.Idempotent = tool.Annotations.IdempotentHint
			annotations.OpenWorld = tool.Annotations.OpenWorldHint
		}
		out = append(out, catalog.Tool{
			ID:          s.cfg.Name + "." + tool.Name,
			Server:      s.cfg.Name,
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			InputSchema: append([]byte(nil), tool.InputSchema...),
			Annotations: annotations,
		})
	}
	return out
}

func (s *mcpSource) Call(ctx context.Context, name string, args json.RawMessage) (catalog.Result, error) {
	client, err := s.ensureClient(ctx)
	if err != nil {
		return catalog.Result{}, err
	}
	result, err := client.CallTool(ctx, name, args)
	if err != nil {
		s.mu.Lock()
		if s.client == client {
			_ = s.client.Close()
			s.client = nil
		}
		s.mu.Unlock()
		return catalog.Result{}, err
	}
	text := result.Text()
	if text == "" && !result.IsError {
		text = "error: tool returned no output"
		return catalog.Result{Text: text, IsError: true}, nil
	}
	return catalog.Result{Text: text, IsError: result.IsError}, nil
}

func (s *mcpSource) ensureClient(ctx context.Context) (*mcp.Client, error) {
	s.mu.Lock()
	if s.client != nil {
		client := s.client
		s.mu.Unlock()
		return client, nil
	}
	s.mu.Unlock()
	client, err := mcp.New(s.cfg)
	if err != nil {
		return nil, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.client != nil {
		existing := s.client
		s.mu.Unlock()
		_ = client.Close()
		return existing, nil
	}
	s.client = client
	s.mu.Unlock()
	return client, nil
}

func (s *mcpSource) Close() error {
	s.mu.Lock()
	client := s.client
	s.client = nil
	s.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

func (s *mcpSource) cachePath() string {
	return filepath.Join(s.cacheDir, s.cfg.Name+".json")
}

func (s *mcpSource) readCache() (mcpCache, bool) {
	data, err := os.ReadFile(s.cachePath())
	if err != nil {
		return mcpCache{}, false
	}
	var cache mcpCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return mcpCache{}, false
	}
	if cache.Version != mcpCacheVersion || cache.Fingerprint != s.fingerprint {
		return mcpCache{}, false
	}
	ttl := cache.TTLMS
	if ttl <= 0 {
		ttl = mcpDefaultTTL
	}
	age := time.Now().UnixMilli() - cache.FetchedAt
	if age < 0 || age > ttl {
		return mcpCache{}, false
	}
	return cache, true
}

func (s *mcpSource) writeCache(ttl time.Duration) {
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return
	}
	ttlMS := int64(mcpDefaultTTL)
	if ttl > 0 {
		ttlMS = ttl.Milliseconds()
	}
	cache := mcpCache{
		Version:      mcpCacheVersion,
		Fingerprint:  s.fingerprint,
		FetchedAt:    time.Now().UnixMilli(),
		TTLMS:        ttlMS,
		Instructions: s.instructions,
		Tools:        s.tools,
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(s.cacheDir, ".mcp-cache-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	_ = os.Chmod(name, 0o600)
	if err := os.Rename(name, s.cachePath()); err != nil {
		_ = os.Remove(name)
	}
}

func configFingerprint(cfg mcp.ServerConfig) string {
	envKeys := make([]string, 0, len(cfg.Env))
	for _, entry := range cfg.Env {
		key, _, _ := strings.Cut(entry, "=")
		envKeys = append(envKeys, key)
	}
	headerKeys := make([]string, 0, len(cfg.Headers))
	for key := range cfg.Headers {
		headerKeys = append(headerKeys, key)
	}
	sort.Strings(envKeys)
	sort.Strings(headerKeys)
	payload := struct {
		Name       string   `json:"name"`
		Command    string   `json:"command"`
		Args       []string `json:"args"`
		URL        string   `json:"url"`
		EnvKeys    []string `json:"env_keys"`
		HeaderKeys []string `json:"header_keys"`
	}{cfg.Name, cfg.Command, cfg.Args, cfg.URL, envKeys, headerKeys}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// buildSources assembles local providers and configured MCP servers.
func buildSources(fc fileConfig, bot *botCtx, refresh bool) ([]catalog.Source, error) {
	var sources []catalog.Source
	used := map[string]bool{}
	for _, command := range localCommands(fc) {
		name := localSourceName(command, used)
		sources = append(sources, newLocalSource(command, name, bot.Workspace))
	}
	configs, err := resolveMCPEntries(fc.mcp, bot.Workspace)
	if err != nil {
		for _, source := range sources {
			_ = source.Close()
		}
		return nil, err
	}
	cacheDir := filepath.Join(bot.DataRoot, "state", "ax", "mcp")
	for _, cfg := range configs {
		if used[cfg.Name] {
			for _, source := range sources {
				_ = source.Close()
			}
			return nil, fmt.Errorf("mcp: server name %q collides with another source", cfg.Name)
		}
		used[cfg.Name] = true
		sources = append(sources, newMCPSource(cfg, cacheDir, refresh))
	}
	return sources, nil
}

func localCommands(fc fileConfig) []string {
	if value, present := os.LookupEnv("AX_TOOLS"); present {
		return strings.Fields(value)
	}
	return strings.Fields(fc.tools)
}

func localSourceName(command string, used map[string]bool) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	var out strings.Builder
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	name := out.String()
	if name == "" || name == "." || name == "/" {
		name = "provider"
	}
	candidate := name
	for i := 2; used[candidate]; i++ {
		candidate = fmt.Sprintf("%s-%d", name, i)
	}
	used[candidate] = true
	return candidate
}

func oneLineText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max > 0 && len([]rune(value)) > max {
		value = string([]rune(value)[:max]) + "..."
	}
	return value
}
