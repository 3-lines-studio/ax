// Command fake-mcp-server is a minimal, real MCP server used only by
// mcpx_test.go, so mcpx's client side is exercised against an actual MCP
// server (not a hand-rolled stand-in) without depending on any external
// service such as buzz-cli.
package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-mcp-server", Version: "test"}, nil)

	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Echoes back the given message.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Message}},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "fail",
		Description: "Always returns a tool-level error.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "deliberately failed"}},
			IsError: true,
		}, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
