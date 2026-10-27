package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// serverConfig is mcpx's view of one entry in MCPX_SERVERS_FILE. Its JSON
// shape matches the wire format of ACP's McpServer stdio variant (no "type"
// key on the wire for stdio -- see acp-go-sdk's McpServer.MarshalJSON):
// acpi writes exactly this shape when it bridges session/new's mcpServers.
// mcpx has no dependency on acpi or ACP itself; this is just the config
// contract, plain JSON.
type serverConfig struct {
	Type    string   `json:"type,omitempty"` // absent or "stdio" in v1; anything else is rejected
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []envVar `json:"env"`
}

type envVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// toolSeparator joins a server's name and a tool's name into the qualified
// name mcpx exposes to ax, e.g. "buzz-cli__send_message". Always qualified,
// even when there's only one server configured, so the tool namespace
// doesn't change shape the moment a second server is added.
const toolSeparator = "__"

// loadServers reads and validates MCPX_SERVERS_FILE. An unset or empty path
// is not an error: it means mcpx has nothing to offer for this invocation
// (describe prints zero tools; run will fail with "unknown server" if
// actually called, which shouldn't happen if AX_TOOLS and
// MCPX_SERVERS_FILE are kept in sync by whatever configures ax).
func loadServers() ([]serverConfig, error) {
	path := os.Getenv("MCPX_SERVERS_FILE")
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mcpx: reading MCPX_SERVERS_FILE %q: %w", path, err)
	}
	var servers []serverConfig
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("mcpx: parsing MCPX_SERVERS_FILE %q: %w", path, err)
	}
	seen := make(map[string]bool, len(servers))
	for _, s := range servers {
		if s.Type != "" && s.Type != "stdio" {
			return nil, fmt.Errorf("mcpx: server %q: transport %q is not supported yet (only stdio)", s.Name, s.Type)
		}
		if s.Name == "" {
			return nil, fmt.Errorf("mcpx: a server entry in %q is missing a name", path)
		}
		if strings.Contains(s.Name, toolSeparator) {
			return nil, fmt.Errorf("mcpx: server name %q must not contain %q (used to qualify tool names)", s.Name, toolSeparator)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("mcpx: duplicate server name %q in %q", s.Name, path)
		}
		seen[s.Name] = true
	}
	return servers, nil
}

func serverByName(servers []serverConfig, name string) (serverConfig, bool) {
	for _, s := range servers {
		if s.Name == name {
			return s, true
		}
	}
	return serverConfig{}, false
}
