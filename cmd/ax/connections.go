package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/3-lines-studio/ax/internal/catalog"
	"github.com/3-lines-studio/ax/internal/mcp"
)

// A connection is the unit of configuration: it names an integration, says how
// to reach it and optionally narrows which of its tools the model may use. The
// tools themselves are discovered from the provider, never listed by hand.
//
// Legacy spellings compile to the same thing, so the mapping is one-way and
// old bots keep working:
//
//	tools = ["wax"]            -> connection{id: "wax", kind: "local"}
//	[[mcp]] name = "github"    -> connection{id: "github", kind: "mcp"}
const (
	connLocal = "local"
	connMCP   = "mcp"
)

// connectionKindNormal maps every accepted [[connection]] kind to the two
// transports the catalog knows today. New transports (ssh, container, volume)
// are a later phase: they change how a connection reaches its tools, not what
// the model sees.
func connectionKindNormal(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "local", "provider":
		return connLocal, nil
	case "mcp", "mcp-stdio", "mcp-http":
		return connMCP, nil
	default:
		return "", fmt.Errorf("unknown kind %q (use local, mcp-stdio or mcp-http)", kind)
	}
}

// connectionEntry is one [[connection]] table in bot.toml.
type connectionEntry struct {
	ID              string            `toml:"id"`
	Kind            string            `toml:"kind"`
	Trust           string            `toml:"trust"`
	Compute         bool              `toml:"compute"`
	Root            string            `toml:"root"`
	Capabilities    []string          `toml:"capabilities"`
	Command         string            `toml:"command"`
	Args            []string          `toml:"args"`
	PassEnv         []string          `toml:"pass_env"`
	Env             map[string]string `toml:"env"`
	URL             string            `toml:"url"`
	TokenEnv        string            `toml:"token_env"`
	Headers         map[string]string `toml:"headers"`
	Timeout         int               `toml:"timeout"`
	Auth            string            `toml:"auth"`
	ClientID        string            `toml:"client_id"`
	ClientSecretEnv string            `toml:"client_secret_env"`
	Scopes          []string          `toml:"scopes"`
}

// mcpEntry flattens a connection into the [[mcp]] shape the transport already
// understands, so the MCP code path is untouched.
func (e connectionEntry) mcpEntry(id string) mcpEntry {
	return mcpEntry{
		Name:            id,
		Command:         e.Command,
		Args:            append([]string(nil), e.Args...),
		PassEnv:         append([]string(nil), e.PassEnv...),
		Env:             e.Env,
		URL:             e.URL,
		TokenEnv:        e.TokenEnv,
		Headers:         e.Headers,
		Timeout:         e.Timeout,
		Auth:            e.Auth,
		ClientID:        e.ClientID,
		ClientSecretEnv: e.ClientSecretEnv,
		Scopes:          append([]string(nil), e.Scopes...),
	}
}

// trust levels. "auto" is the default: the connection exposes every tool it
// declares. "read" keeps only the tools annotated read-only, which is the safe
// default for an integration whose writes are not wanted (a repository on a
// shared account, a production database).
const (
	trustAuto = "auto"
	trustRead = "read"
)

func normaliseTrust(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "full":
		return trustAuto, nil
	case "read", "readonly", "read-only":
		return trustRead, nil
	default:
		return "", fmt.Errorf("unknown trust %q (use auto or read)", value)
	}
}

// connection is a resolved source: either a local provider command or an MCP
// server config. A per-connection error keeps the integration visible in the
// menu instead of aborting every other source.
type connection struct {
	id      string
	kind    string
	trust   string
	compute bool
	command string
	cfg     mcp.ServerConfig
	entry   connectionEntry // as written in bot.toml, for ax conn list
	caps    []string
	err     error
}

// resolveConnections merges explicit [[connection]] tables with the legacy
// tools/AX_TOOLS list and [[mcp]] tables. Explicit ids win, legacy ids keep the
// names they had before (a provider named after its binary), so catalog ids and
// stored sessions stay valid.
func resolveConnections(fc fileConfig, workdir, authDir string) ([]connection, error) {
	var out []connection
	used := map[string]bool{}

	for _, entry := range fc.connections {
		id := strings.ToLower(strings.TrimSpace(entry.ID))
		if id == "" {
			return nil, fmt.Errorf("connection: every [[connection]] table needs an id")
		}
		if err := validServerName(id); err != nil {
			return nil, fmt.Errorf("connection %q: %w", id, err)
		}
		if used[id] {
			return nil, fmt.Errorf("connection: duplicate connection id %q", id)
		}
		used[id] = true

		kind, err := connectionKindNormal(entry.Kind)
		if err != nil {
			return nil, fmt.Errorf("connection %q: %w", id, err)
		}
		caps, err := normaliseCapabilities(entry.Capabilities)
		if err != nil {
			return nil, fmt.Errorf("connection %q: %w", id, err)
		}
		trust, err := normaliseTrust(entry.Trust)
		if err != nil {
			return nil, fmt.Errorf("connection %q: %w", id, err)
		}
		if entry.Root != "" && !entry.Compute {
			return nil, fmt.Errorf("connection %q: root only applies to compute = true", id)
		}
		conn := connection{id: id, kind: kind, trust: trust, compute: entry.Compute, entry: entry, caps: caps}
		switch kind {
		case connLocal:
			if entry.Command == "" {
				return nil, fmt.Errorf("connection %q: kind local needs a command", id)
			}
			if entry.URL != "" {
				return nil, fmt.Errorf("connection %q: kind local cannot use url (use kind mcp-http)", id)
			}
			if len(entry.Args) > 0 {
				return nil, fmt.Errorf("connection %q: local providers take no args; wrap the command in a script", id)
			}
			conn.command = entry.Command
		case connMCP:
			mapped := entry.mcpEntry(id)
			if (mapped.Command == "") == (mapped.URL == "") {
				return nil, fmt.Errorf("connection %q: needs exactly one of command or url", id)
			}
			oauth, err := wantsOAuth(mapped)
			if err != nil {
				return nil, fmt.Errorf("connection %q: %w", id, err)
			}
			cfg, err := resolveMCPEntry(id, mapped, workdir, authDir, oauth)
			if err != nil {
				conn.err = err
			} else {
				conn.cfg = cfg
			}
		}
		out = append(out, conn)
	}

	for _, command := range localCommands(fc) {
		name := localSourceName(command, used)
		out = append(out, connection{id: name, kind: connLocal, trust: trustAuto, command: command})
	}

	var legacy []mcpEntry
	for _, entry := range fc.mcp {
		if id := strings.ToLower(strings.TrimSpace(entry.Name)); id != "" && used[id] {
			// An explicit [[connection]] with the same id already owns it.
			return nil, fmt.Errorf("mcp: server name %q collides with a connection id", id)
		}
		legacy = append(legacy, entry)
	}
	resolved, err := resolveMCPEntries(legacy, workdir, authDir)
	if err != nil {
		return nil, err
	}
	for _, server := range resolved {
		if used[server.name] {
			return nil, fmt.Errorf("mcp: server name %q collides with another source", server.name)
		}
		used[server.name] = true
		out = append(out, connection{id: server.name, kind: connMCP, trust: trustAuto, cfg: server.cfg, err: server.err})
	}
	return out, nil
}

// computeConfig is the bot's compute role: the connections whose tools become
// the default core tools, and the directory they run in. It is the seam where a
// local workspace becomes a remote one later: `root` is a path on whichever
// backend the connection reaches, so nothing else in the engine has to change.
type computeConfig struct {
	ids  []string
	root string
}

// resolveCompute validates the [[connection]] tables marked compute = true.
// Several are allowed (fsx for files, bashx for the shell), but they must agree
// on where the workspace lives.
func resolveCompute(fc fileConfig, botRoot string) (computeConfig, error) {
	var out computeConfig
	for _, entry := range fc.connections {
		id := strings.ToLower(strings.TrimSpace(entry.ID))
		if !entry.Compute {
			if strings.TrimSpace(entry.Root) != "" {
				return computeConfig{}, fmt.Errorf("connection %q: root only applies to compute = true", id)
			}
			continue
		}
		if id == "" {
			return computeConfig{}, fmt.Errorf("connection: a compute connection needs an id")
		}
		out.ids = append(out.ids, id)
		if strings.TrimSpace(entry.Root) == "" {
			continue
		}
		root := strings.TrimSpace(entry.Root)
		if !filepath.IsAbs(root) {
			root = filepath.Join(botRoot, root)
		}
		root = filepath.Clean(root)
		if out.root != "" && out.root != root {
			return computeConfig{}, fmt.Errorf("connection %q: root %s disagrees with another compute connection", id, root)
		}
		info, err := os.Stat(root)
		if err != nil {
			return computeConfig{}, fmt.Errorf("connection %q: root %s: %w", id, root, err)
		}
		if !info.IsDir() {
			return computeConfig{}, fmt.Errorf("connection %q: root %s is not a directory", id, root)
		}
		out.root = root
	}
	return out, nil
}

// normaliseCapabilities validates one connection's optional allowlist. An empty
// list means "every tool this connection declares", which is the common case.
func normaliseCapabilities(caps []string) ([]string, error) {
	if len(caps) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(caps))
	for _, cap := range caps {
		cap = strings.ToLower(strings.TrimSpace(cap))
		if cap == "" {
			continue
		}
		if strings.ContainsAny(cap, " \t/") {
			return nil, fmt.Errorf("capability %q: use <connection>.<tool> or <tool>", cap)
		}
		if seen[cap] {
			continue
		}
		seen[cap] = true
		out = append(out, cap)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// connSource narrows a source to what its connection enables: the
// `capabilities` allowlist, the `trust` level, or both. The menu only lists the
// enabled tools and a call to a disabled one names the reason.
type connSource struct {
	inner    catalog.Source
	id       string
	trust    string
	caps     map[string]bool
	mu       sync.Mutex
	enabled  map[string]bool // tool name -> allowed, filled by List
	declared int
}

func newConnSource(inner catalog.Source, conn connection) catalog.Source {
	if len(conn.caps) == 0 && conn.trust != trustRead {
		return inner
	}
	allowed := make(map[string]bool, len(conn.caps))
	for _, cap := range conn.caps {
		allowed[strings.ToLower(cap)] = true
	}
	trust := conn.trust
	if trust == "" {
		trust = trustAuto
	}
	return &connSource{inner: inner, id: strings.ToLower(conn.id), trust: trust, caps: allowed}
}

func (s *connSource) Name() string        { return s.inner.Name() }
func (s *connSource) Description() string { return s.inner.Description() }
func (s *connSource) Close() error        { return s.inner.Close() }

// bare drops the connection prefix so both "search" and "web.search" reach the
// inner source as the tool name it knows.
func (s *connSource) bare(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.TrimPrefix(name, s.id+".")
}

// allowsCap reports whether the capabilities allowlist names this tool.
func (s *connSource) allowsCap(bare string) bool {
	if len(s.caps) == 0 {
		return true
	}
	return s.caps[s.id+"."+bare] || s.caps[bare]
}

func (s *connSource) List(ctx context.Context) ([]catalog.Tool, error) {
	tools, err := s.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(tools))
	out := make([]catalog.Tool, 0, len(tools))
	for _, tool := range tools {
		bare := strings.ToLower(tool.Name)
		if !s.allowsCap(bare) {
			continue
		}
		if s.trust == trustRead && !tool.Annotations.ReadOnly {
			continue
		}
		enabled[bare] = true
		out = append(out, tool)
	}
	s.mu.Lock()
	s.enabled = enabled
	s.declared = len(tools)
	s.mu.Unlock()
	// The filtered tool list is what the catalog indexes, so it must carry the
	// count the menu prints.
	return out, nil
}

func (s *connSource) Call(ctx context.Context, name string, args json.RawMessage) (catalog.Result, error) {
	bare := s.bare(name)
	s.mu.Lock()
	enabled := s.enabled
	s.mu.Unlock()
	if enabled == nil {
		// A call before the catalog listed this source: list once so trust and
		// capabilities are enforced the same way.
		if _, err := s.List(ctx); err != nil {
			return catalog.Result{}, err
		}
		s.mu.Lock()
		enabled = s.enabled
		s.mu.Unlock()
	}
	if !enabled[bare] {
		return catalog.Result{}, s.refusal(name)
	}
	return s.inner.Call(ctx, bare, args)
}

func (s *connSource) refusal(name string) error {
	bare := s.bare(name)
	if s.trust == trustRead {
		if !s.allowsCap(bare) {
			return fmt.Errorf("connection %q does not enable %q and is read-only", s.id, name)
		}
		return fmt.Errorf("connection %q is read-only: %q is not available", s.id, name)
	}
	enabled := make([]string, 0, len(s.enabled))
	for tool := range s.enabled {
		enabled = append(enabled, s.id+"."+tool)
	}
	sort.Strings(enabled)
	return fmt.Errorf("connection %q does not enable %q (enabled: %s)", s.id, name, strings.Join(enabled, ", "))
}

// RequiresAuth forwards the MCP login state so the menu can still say
// "login required".
func (s *connSource) RequiresAuth() bool {
	if status, ok := s.inner.(catalog.AuthStatus); ok {
		return status.RequiresAuth()
	}
	return false
}
