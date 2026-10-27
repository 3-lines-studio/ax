package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/mcp"
)

// mcpEntry is one [[mcp]] table in bot.toml.
type mcpEntry struct {
	Name     string            `toml:"name"`
	Command  string            `toml:"command"`
	Args     []string          `toml:"args"`
	PassEnv  []string          `toml:"pass_env"`
	Env      map[string]string `toml:"env"`
	URL      string            `toml:"url"`
	TokenEnv string            `toml:"token_env"`
	Headers  map[string]string `toml:"headers"`
	Timeout  int               `toml:"timeout"`
}

var defaultCoreTools = []string{"fsx.read", "fsx.write", "fsx.edit", "bashx.bash"}

func coreTools(fc fileConfig) []string {
	if len(fc.coreTools) > 0 {
		return fc.coreTools
	}
	return defaultCoreTools
}

func validateToolMode(cfg cliConfig, fc fileConfig) error {
	switch strings.ToLower(firstNonEmpty(cfg.toolMode, fc.toolMode, "catalog")) {
	case "catalog", "direct":
		return nil
	default:
		return fmt.Errorf("invalid -tool-mode: %s (use catalog or direct)", firstNonEmpty(cfg.toolMode, fc.toolMode))
	}
}

func resolveMCPEntries(entries []mcpEntry, workdir string) ([]mcp.ServerConfig, error) {
	seen := map[string]bool{}
	out := make([]mcp.ServerConfig, 0, len(entries))
	for _, entry := range entries {
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if name == "" {
			return nil, fmt.Errorf("mcp: every [[mcp]] table needs a name")
		}
		if err := validServerName(name); err != nil {
			return nil, fmt.Errorf("mcp: server %q: %w", name, err)
		}
		if seen[name] {
			return nil, fmt.Errorf("mcp: duplicate server name %q", name)
		}
		seen[name] = true
		if (entry.Command == "") == (entry.URL == "") {
			return nil, fmt.Errorf("mcp: server %q needs exactly one of command or url", name)
		}
		cfg := mcp.ServerConfig{
			Name:    name,
			Command: entry.Command,
			Args:    append([]string(nil), entry.Args...),
			Dir:     workdir,
			URL:     entry.URL,
			Headers: map[string]string{},
		}
		for _, key := range entry.PassEnv {
			if !validEnvName(key) {
				return nil, fmt.Errorf("mcp: server %q: invalid pass_env name %q", name, key)
			}
			if value, ok := os.LookupEnv(key); ok {
				cfg.Env = append(cfg.Env, key+"="+value)
			}
		}
		for key, value := range entry.Env {
			if !validEnvName(key) {
				return nil, fmt.Errorf("mcp: server %q: invalid env name %q", name, key)
			}
			expanded, err := expandEnvValue(value)
			if err != nil {
				return nil, fmt.Errorf("mcp: server %q: env %s: %w", name, key, err)
			}
			cfg.Env = append(cfg.Env, key+"="+expanded)
		}
		for key, value := range entry.Headers {
			expanded, err := expandEnvValue(value)
			if err != nil {
				return nil, fmt.Errorf("mcp: server %q: header %s: %w", name, key, err)
			}
			cfg.Headers[key] = expanded
		}
		if entry.TokenEnv != "" {
			if entry.URL == "" {
				return nil, fmt.Errorf("mcp: server %q: token_env is only for url servers", name)
			}
			if !validEnvName(entry.TokenEnv) {
				return nil, fmt.Errorf("mcp: server %q: invalid token_env name %q", name, entry.TokenEnv)
			}
			token := os.Getenv(entry.TokenEnv)
			if token == "" {
				return nil, fmt.Errorf("mcp: server %q: %s is not set", name, entry.TokenEnv)
			}
			if _, exists := cfg.Headers["Authorization"]; exists {
				return nil, fmt.Errorf("mcp: server %q: token_env and an Authorization header are mutually exclusive", name)
			}
			cfg.Headers["Authorization"] = "Bearer " + token
		}
		timeout := entry.Timeout
		if timeout <= 0 {
			timeout = 30
		}
		if timeout > 600 {
			return nil, fmt.Errorf("mcp: server %q: timeout %ds exceeds 600", name, timeout)
		}
		cfg.Timeout = time.Duration(timeout) * time.Second
		if _, err := mcp.New(cfg); err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, nil
}

func validServerName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("name must be 1-64 characters")
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-' {
			continue
		}
		return fmt.Errorf("name may use lowercase letters, digits, _ and -")
	}
	return nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' || (i > 0 && b >= '0' && b <= '9') {
			continue
		}
		return false
	}
	return true
}

func expandEnvValue(value string) (string, error) {
	var missing string
	out := os.Expand(value, func(key string) string {
		resolved, ok := os.LookupEnv(key)
		if !ok {
			missing = key
			return ""
		}
		return resolved
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return out, nil
}
