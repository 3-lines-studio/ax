package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type botConfig struct {
	Model   string   `toml:"model"`
	BaseURL string   `toml:"base_url"`
	Tools   []string `toml:"tools"`
}

type botCtx struct {
	Root      string
	Workspace string
	Skills    string
	Prompt    string
	APIKey    string
	HasAPIKey bool
	IsBot     bool
}

func loadBotConfig(root string) (botConfig, error) {
	data, err := os.ReadFile(filepath.Join(root, "bot.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return botConfig{}, nil
		}
		return botConfig{}, fmt.Errorf("read bot.toml: %w", err)
	}
	var bc botConfig
	if err := toml.Unmarshal(data, &bc); err != nil {
		return botConfig{}, fmt.Errorf("parse bot.toml: %w", err)
	}
	return bc, nil
}

func resolveBotCtx(root string) *botCtx {
	ctx := &botCtx{Root: root}
	if pathExists(filepath.Join(root, "bot.toml")) {
		ctx.IsBot = true
	}
	dataRoot := botDataRoot(root)
	workspace := filepath.Join(dataRoot, "workspace")
	if info, err := os.Stat(workspace); err == nil && info.IsDir() {
		ctx.Workspace = workspace
	} else if ctx.IsBot {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: create workspace: %v\n", err)
		}
		ctx.Workspace = workspace
	}
	if ctx.Workspace == "" {
		ctx.Workspace = dataRoot
	}
	if skills := filepath.Join(root, "skills"); pathIsDir(skills) {
		ctx.Skills = skills
	}
	if prompt := filepath.Join(root, "bot.md"); pathExists(prompt) {
		if data, err := os.ReadFile(prompt); err == nil {
			ctx.Prompt = strings.TrimSpace(string(data))
		}
	}
	if key := filepath.Join(root, "secrets", "api-key"); pathExists(key) {
		if data, err := os.ReadFile(key); err == nil {
			ctx.APIKey = strings.TrimSpace(string(data))
			ctx.HasAPIKey = true
		}
	}
	return ctx
}

func mergeBotConfig(fc fileConfig, bc botConfig) fileConfig {
	if bc.Model != "" {
		fc.model = bc.Model
	}
	if bc.BaseURL != "" {
		fc.base = bc.BaseURL
	}
	if len(bc.Tools) > 0 {
		fc.tools = strings.Join(bc.Tools, " ")
	}
	return fc
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func botDataRoot(root string) string {
	if d := os.Getenv("BOT_DATA"); d != "" {
		return d
	}
	return root
}

func pathIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func botRootArg(args []string) string {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" || a == "" || !strings.HasPrefix(a, "-") {
			break
		}
		stripped := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		name, inline, hasInline := stripped, "", false
		if idx := strings.IndexByte(stripped, '='); idx >= 0 {
			name, inline, hasInline = stripped[:idx], stripped[idx+1:], true
		}
		if name == "C" {
			if hasInline {
				return inline
			}
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		switch name {
		case "base", "model", "system", "session":
			if !hasInline {
				i++
			}
		}
		i++
	}
	return ""
}
