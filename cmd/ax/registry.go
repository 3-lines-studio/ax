package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// mcpsJSON is the curated registry shipped with the binary. AX_MCP_REGISTRY
// points at a replacement file, which is what the tests use.
//
//go:embed mcps.json
var mcpsJSON []byte

// registryEntry is one known MCP server.
type registryEntry struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	URL             string            `json:"url,omitempty"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Auth            string            `json:"auth,omitempty"`
	ClientIDEnv     string            `json:"client_id_env,omitempty"`
	ClientSecretEnv string            `json:"client_secret_env,omitempty"`
	Needs           string            `json:"needs,omitempty"`
	Docs            string            `json:"docs,omitempty"`
}

type registryFile struct {
	Version int             `json:"version"`
	Servers []registryEntry `json:"servers"`
}

var (
	registryOnce sync.Once
	registryData registryFile
	registryErr  error
)

func loadRegistry() (registryFile, error) {
	registryOnce.Do(func() {
		data := mcpsJSON
		if path := os.Getenv("AX_MCP_REGISTRY"); path != "" {
			override, err := os.ReadFile(path)
			if err != nil {
				registryErr = fmt.Errorf("read AX_MCP_REGISTRY: %w", err)
				return
			}
			data = override
		}
		if err := json.Unmarshal(data, &registryData); err != nil {
			registryErr = fmt.Errorf("parse MCP registry: %w", err)
		}
	})
	return registryData, registryErr
}

func (r registryFile) find(name string) (registryEntry, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, entry := range r.Servers {
		if strings.ToLower(entry.Name) == name {
			return entry, true
		}
	}
	return registryEntry{}, false
}

// entry converts a registry entry into the bot.toml table it would write.
func (e registryEntry) entry(extraArgs []string) mcpEntry {
	entry := mcpEntry{
		Name:            e.Name,
		Command:         e.Command,
		Args:            append(append([]string(nil), e.Args...), extraArgs...),
		URL:             e.URL,
		Headers:         e.Headers,
		Env:             e.Env,
		ClientSecretEnv: e.ClientSecretEnv,
	}
	if e.ClientIDEnv != "" {
		entry.ClientID = "${" + e.ClientIDEnv + "}"
	}
	if e.URL != "" && e.Auth == "none" {
		entry.Auth = "none"
	}
	return entry
}
