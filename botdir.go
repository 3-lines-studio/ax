package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// botConfig holds the Botdir bot.toml keys owned by the ax consumer. Root
// keys are reserved by Botdir; unknown keys and tables (e.g. [slack]) are
// ignored for forward compatibility.
type botConfig struct {
	Model   string   `toml:"model"`
	BaseURL string   `toml:"base_url"`
	Tools   []string `toml:"tools"`
}

// botCtx is the resolved Botdir context for one ax run.
type botCtx struct {
	Root      string // absolute bot root (initial CWD or -C)
	Workspace string // model work area: Root/workspace when present/used, else Root
	Skills    string // Root/skills; tool providers derive it from BOT_ROOT
	Prompt    string // Root/bot.md content, appended to the system prompt
	APIKey    string // Root/secrets/api-key value
	HasAPIKey bool
	IsBot     bool // Root/bot.toml present
}

// loadBotConfig reads Root/bot.toml if present. A missing file is an empty
// config; an unreadable or invalid file is an error.
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

// resolveBotCtx fills the Botdir context for a bot root.
func resolveBotCtx(root string) *botCtx {
	ctx := &botCtx{Root: root}
	if pathExists(filepath.Join(root, "bot.toml")) {
		ctx.IsBot = true
	}
	workspace := filepath.Join(root, "workspace")
	if info, err := os.Stat(workspace); err == nil && info.IsDir() {
		ctx.Workspace = workspace
	} else if ctx.IsBot {
		// A runtime bot builds its mutable paths on first run.
		_ = os.MkdirAll(workspace, 0o755)
		ctx.Workspace = workspace
	}
	if ctx.Workspace == "" {
		ctx.Workspace = root
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

// mergeBotConfig overlays bot.toml values over the legacy file config.
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

func pathIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// botRootArg returns the -C/--C value from args before the prompt starts,
// without fully parsing them. It returns "" when no directory flag is given.
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
