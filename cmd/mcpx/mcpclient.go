package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectServer spawns one MCP server per serverConfig and completes the MCP
// handshake. mcpx has no persistent process of its own -- ax execs it fresh
// per describe/run call -- so every call to this function starts and tears
// down its own copy of the underlying server. See the README's "Known
// limitations" section on the resulting per-call handshake overhead.
func connectServer(ctx context.Context, cfg serverConfig) (*mcp.ClientSession, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for _, e := range cfg.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	cmd.Stderr = os.Stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpx", Version: version}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("mcpx: connecting to server %q: %w", cfg.Name, err)
	}
	return session, nil
}
