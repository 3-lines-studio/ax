package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/mcp"
)

type mcpEntry struct {
	Name            string            `toml:"name"`
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

var defaultCoreTools = []string{"fsx.read", "fsx.write", "fsx.edit", "bashx.bash"}

func validateToolMode(cfg cliConfig, fc fileConfig) error {
	switch strings.ToLower(firstNonEmpty(cfg.toolMode, fc.toolMode, "catalog")) {
	case "catalog", "direct":
		return nil
	default:
		return fmt.Errorf("invalid -tool-mode: %s (use catalog or direct)", firstNonEmpty(cfg.toolMode, fc.toolMode))
	}
}

type resolvedMCP struct {
	name string
	cfg  mcp.ServerConfig
	err  error
}

func oauthTokenPath(authDir, name string) string {
	return filepath.Join(authDir, name+".json")
}

func mcpAuthDir(bot *botCtx) string {
	return filepath.Join(bot.DataRoot, "state", "ax", "mcp", "auth")
}

func wantsOAuth(entry mcpEntry) (bool, error) {
	if entry.URL == "" || entry.TokenEnv != "" {
		return false, nil
	}
	for key := range entry.Headers {
		if strings.EqualFold(key, "Authorization") {
			return false, nil
		}
	}
	switch mode := strings.ToLower(strings.TrimSpace(entry.Auth)); mode {
	case "", "auto", "oauth":
		return true, nil
	case "none", "off", "token":
		return false, nil
	default:
		return false, fmt.Errorf("mcp: server %q: auth must be oauth or none, not %q", entry.Name, entry.Auth)
	}
}

func oauthConfigFor(entry mcpEntry) (mcp.OAuthConfig, error) {
	cfg := mcp.OAuthConfig{ClientID: expandOptional(entry.ClientID), Scopes: append([]string(nil), entry.Scopes...)}
	if entry.ClientSecretEnv != "" {
		if !validEnvName(entry.ClientSecretEnv) {
			return mcp.OAuthConfig{}, fmt.Errorf("mcp: server %q: invalid client_secret_env %q", entry.Name, entry.ClientSecretEnv)
		}
		cfg.ClientSecret = os.Getenv(entry.ClientSecretEnv)
	}
	return cfg, nil
}

func resolveMCPEntries(entries []mcpEntry, workdir, authDir string) ([]resolvedMCP, error) {
	seen := map[string]bool{}
	out := make([]resolvedMCP, 0, len(entries))
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
		oauth, err := wantsOAuth(entry)
		if err != nil {
			return nil, err
		}
		cfg, err := resolveMCPEntry(name, entry, workdir, authDir, oauth)
		if err != nil {
			out = append(out, resolvedMCP{name: name, err: err})
			continue
		}
		out = append(out, resolvedMCP{name: name, cfg: cfg})
	}
	return out, nil
}

func resolveMCPEntry(name string, entry mcpEntry, workdir, authDir string, oauth bool) (mcp.ServerConfig, error) {
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
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: invalid pass_env name %q", name, key)
		}
		if value, ok := os.LookupEnv(key); ok {
			cfg.Env = append(cfg.Env, key+"="+value)
		}
	}
	for key, value := range entry.Env {
		if !validEnvName(key) {
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: invalid env name %q", name, key)
		}
		expanded, err := expandEnvValue(value)
		if err != nil {
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: env %s: %w", name, key, err)
		}
		cfg.Env = append(cfg.Env, key+"="+expanded)
	}
	for key, value := range entry.Headers {
		expanded, err := expandEnvValue(value)
		if err != nil {
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: header %s: %w", name, key, err)
		}
		cfg.Headers[key] = expanded
	}
	if entry.TokenEnv != "" {
		if entry.URL == "" {
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: token_env is only for url servers", name)
		}
		if !validEnvName(entry.TokenEnv) {
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: invalid token_env name %q", name, entry.TokenEnv)
		}
		token := os.Getenv(entry.TokenEnv)
		if token == "" {
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: %s is not set", name, entry.TokenEnv)
		}
		cfg.Headers["Authorization"] = "Bearer " + token
	}
	if cfg.URL != "" {
		expanded, err := expandEnvValue(entry.URL)
		if err != nil {
			return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: url: %w", name, err)
		}
		cfg.URL = expanded
	}
	timeout := entry.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 600 {
		return mcp.ServerConfig{}, fmt.Errorf("mcp: server %q: timeout %ds exceeds 600", name, timeout)
	}
	cfg.Timeout = time.Duration(timeout) * time.Second
	if oauth {
		cfg.Auth = mcp.NewOAuthTokenSource(oauthTokenPath(authDir, name), cfg.URL, nil)
	}
	if _, err := mcp.New(cfg); err != nil {
		return mcp.ServerConfig{}, err
	}
	return cfg, nil
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
	out, missing := expandTemplate(value, os.LookupEnv)
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return out, nil
}

func expandOptional(value string) string {
	out, _ := expandTemplate(value, func(key string) (string, bool) {
		resolved, ok := os.LookupEnv(key)
		return resolved, ok || true
	})
	return out
}

func expandTemplate(value string, lookup func(string) (string, bool)) (string, string) {
	var missing string
	var out strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			out.WriteString(value)
			break
		}
		end := strings.IndexByte(value[start:], '}')
		if end < 0 {
			out.WriteString(value)
			break
		}
		end += start
		out.WriteString(value[:start])
		key, fallback, hasFallback := strings.Cut(value[start+2:end], ":-")
		key = strings.TrimSpace(key)
		resolved, ok := lookup(key)
		switch {
		case ok:
			out.WriteString(resolved)
		case hasFallback:
			out.WriteString(fallback)
		default:
			if missing == "" {
				missing = key
			}
			out.WriteString(value[start : end+1])
		}
		value = value[end+1:]
	}
	return out.String(), missing
}
