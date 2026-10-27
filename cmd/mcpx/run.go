package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runRun calls one tool on one server. ax writes the JSON arguments to
// stdin and reads the result from stdout, treating a non-zero exit as a
// tool error -- see ax's README, "Unix tool convention".
func runRun(ctx context.Context, qualifiedName string) error {
	serverName, toolName, ok := strings.Cut(qualifiedName, toolSeparator)
	if !ok {
		return fmt.Errorf("mcpx: run: %q is not a qualified tool name (want <server>%s<tool>)", qualifiedName, toolSeparator)
	}
	servers, err := loadServers()
	if err != nil {
		return err
	}
	cfg, ok := serverByName(servers, serverName)
	if !ok {
		return fmt.Errorf("mcpx: run: unknown server %q", serverName)
	}

	argsData, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("mcpx: run: reading arguments from stdin: %w", err)
	}
	var args any
	if len(strings.TrimSpace(string(argsData))) > 0 {
		if err := json.Unmarshal(argsData, &args); err != nil {
			return fmt.Errorf("mcpx: run: parsing arguments: %w", err)
		}
	}

	session, err := connectServer(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return fmt.Errorf("mcpx: run: calling %q on %q: %w", toolName, serverName, err)
	}

	text := contentText(result.Content)
	if text == "" && result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return fmt.Errorf("mcpx: run: encoding structured content: %w", err)
		}
		text = string(data)
	}
	fmt.Println(text)

	if result.IsError {
		os.Exit(1)
	}
	return nil
}

func contentText(content []mcp.Content) string {
	var sb strings.Builder
	for _, c := range content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(tc.Text)
	}
	return sb.String()
}
