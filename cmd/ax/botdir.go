package main

import (
	"strings"

	"github.com/3-lines-studio/ax/internal/botdir"
)

type botConfig struct {
	Model           string     `toml:"model"`
	BaseURL         string     `toml:"base_url"`
	Tools           []string   `toml:"tools"`
	Thinking        *bool      `toml:"thinking"`
	ReasoningEffort string     `toml:"reasoning_effort"`
	MaxTokens       int        `toml:"max_tokens"`
	ToolMode        string     `toml:"tool_mode"`
	CoreTools       []string   `toml:"core_tools"`
	MCP             []mcpEntry `toml:"mcp"`
}

// botCtx is what the engine needs from the Botdir conventions.
type botCtx = botdir.Paths

func loadBotConfig(root string) (botConfig, error) {
	var bc botConfig
	err := botdir.LoadTOML(root, &bc)
	return bc, err
}

func resolveBotCtx(root string) *botCtx {
	paths := botdir.Resolve(root)
	return &paths
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
	if bc.Thinking != nil {
		if *bc.Thinking {
			fc.thinking = "on"
		} else {
			fc.thinking = "off"
		}
	}
	if bc.ReasoningEffort != "" {
		fc.effort = bc.ReasoningEffort
	}
	if bc.MaxTokens > 0 {
		fc.maxTokens = bc.MaxTokens
	}
	if bc.ToolMode != "" {
		fc.toolMode = bc.ToolMode
	}
	if len(bc.CoreTools) > 0 {
		fc.coreTools = append([]string(nil), bc.CoreTools...)
	}
	if len(bc.MCP) > 0 {
		fc.mcp = append([]mcpEntry(nil), bc.MCP...)
	}
	return fc
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
		case "base", "model", "system", "session", "thinking", "effort", "max-tokens", "image", "image-detail", "tool-mode":
			if !hasInline {
				i++
			}
		}
		i++
	}
	return ""
}
