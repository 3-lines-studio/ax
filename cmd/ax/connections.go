package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

// connection is a resolved source: either a local provider command or an MCP
// server config. A per-connection error keeps the integration visible in the
// menu instead of aborting every other source.
type connection struct {
	id      string
	kind    string
	command string
	cfg     mcp.ServerConfig
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
		conn := connection{id: id, kind: kind, caps: caps}
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
		out = append(out, connection{id: name, kind: connLocal, command: command})
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
		out = append(out, connection{id: server.name, kind: connMCP, cfg: server.cfg, err: server.err})
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

// cappedSource narrows a source to the capabilities its connection enables.
// The menu only lists the enabled tools and a call to a disabled one is
// rejected with the list that is available.
type cappedSource struct {
	inner catalog.Source
	id    string
	caps  map[string]bool
}

func newCappedSource(inner catalog.Source, id string, caps []string) catalog.Source {
	if len(caps) == 0 {
		return inner
	}
	allowed := make(map[string]bool, len(caps))
	for _, cap := range caps {
		allowed[strings.ToLower(cap)] = true
	}
	return &cappedSource{inner: inner, id: strings.ToLower(id), caps: allowed}
}

func (s *cappedSource) Name() string        { return s.inner.Name() }
func (s *cappedSource) Description() string { return s.inner.Description() }
func (s *cappedSource) Close() error        { return s.inner.Close() }

// bare drops the connection prefix so both "search" and "web.search" reach
// the inner source as the tool name it knows.
func (s *cappedSource) bare(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.TrimPrefix(name, s.id+".")
}

func (s *cappedSource) allows(name string) bool {
	bare := s.bare(name)
	return s.caps[s.id+"."+bare] || s.caps[bare]
}

func (s *cappedSource) List(ctx context.Context) ([]catalog.Tool, error) {
	tools, err := s.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]catalog.Tool, 0, len(tools))
	for _, tool := range tools {
		if s.allows(tool.Name) {
			out = append(out, tool)
		}
	}
	return out, nil
}

func (s *cappedSource) Call(ctx context.Context, name string, args json.RawMessage) (catalog.Result, error) {
	if !s.allows(name) {
		enabled := make([]string, 0, len(s.caps))
		for cap := range s.caps {
			enabled = append(enabled, cap)
		}
		sort.Strings(enabled)
		return catalog.Result{}, fmt.Errorf("connection %q does not enable %q (enabled: %s)",
			s.id, name, strings.Join(enabled, ", "))
	}
	return s.inner.Call(ctx, s.bare(name), args)
}

// RequiresAuth forwards the MCP login state so the menu can still say
// "login required".
func (s *cappedSource) RequiresAuth() bool {
	if status, ok := s.inner.(catalog.AuthStatus); ok {
		return status.RequiresAuth()
	}
	return false
}
