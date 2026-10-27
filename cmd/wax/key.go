package main

import (
	"os"
	"path/filepath"
	"strings"
)

// The Search API uses the same bearer key as the model API, so wax resolves the
// key exactly like ax does: if the bot already talks to DeepSeek, web_search
// works without exporting anything new.
//
// Order (first hit wins):
//
//  1. DEEPSEEK_API_KEY
//  2. OPENAI_API_KEY            (ax accepts it for DeepSeek endpoints)
//  3. $BOT_ROOT/secrets/api-key (the Botdir secret ax reads)
//  4. ~/.config/ax/config       (api_key = ...)
func resolveDeepSeekKey() string {
	for _, name := range []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	root := strings.TrimSpace(os.Getenv("BOT_ROOT"))
	if root == "" {
		root, _ = os.Getwd()
	}
	if root != "" {
		if data, err := os.ReadFile(filepath.Join(root, "secrets", "api-key")); err == nil {
			if value := strings.TrimSpace(string(data)); value != "" {
				return value
			}
		}
	}
	return configKey()
}

// configKey reads api_key from the same file ax reads, honoring
// XDG_CONFIG_HOME.
func configKey() string {
	dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	data, err := os.ReadFile(filepath.Join(dir, "ax", "config"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "api_key" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		if value != "" {
			return value
		}
	}
	return ""
}

// keyHint tells the user where web_search expects a key.
const keyHint = "set DEEPSEEK_API_KEY, or api_key in ~/.config/ax/config, or secrets/api-key in the bot"
