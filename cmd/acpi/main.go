// Command acpi bridges the Agent Client Protocol (ACP) to ax, the AX
// ecosystem's headless agent harness. It speaks ACP on stdio (so any
// ACP-hosting client -- buzz-acp, Zed, etc. -- can spawn it) and runs ax as
// a subprocess per turn, translating between the two wire protocols.
//
// ax has no ACP support of its own (it speaks its own --events JSONL
// protocol) and no MCP client (its tools are external Unix-convention
// commands), so acpi's Agent implementation (agent.go) and event translator
// (axrun.go) exist to cover that gap. Bridging the MCP servers a client
// injects via session/new into ax-callable tools is mcpx's job, not
// implemented here yet -- see the NewSession and runAx doc comments.
package main

import (
	"log/slog"
	"os"

	acp "github.com/coder/acp-go-sdk"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("acpi: config", "error", err)
		os.Exit(1)
	}

	ag := newAgent(cfg)
	conn := acp.NewAgentSideConnection(ag, os.Stdout, os.Stdin)
	conn.SetLogger(logger)
	ag.SetAgentConnection(conn)

	slog.Info("acpi: ready", "ax", cfg.AxPath, "model", cfg.Model, "base", cfg.Base, "sessionDir", cfg.SessionDir)
	<-conn.Done()
}
