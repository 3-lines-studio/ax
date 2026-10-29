package main

import (
	"os"
	"path/filepath"
	"strings"
)

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

const keyHint = "set DEEPSEEK_API_KEY, or api_key in ~/.config/ax/config, or secrets/api-key in the bot"
