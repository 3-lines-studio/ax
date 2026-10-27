package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/3-lines-studio/ax/internal/mcp"
)

const maxMCPMessage = 32 << 20

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// serveMCP exposes search and execute as an MCP stdio server. The same two
// tools the engine shows its model are thus available to any MCP host.
func serveMCP(input io.Reader, output io.Writer, tools []Tool) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxMCPMessage)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request mcpRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_ = encoder.Encode(rpcError(nil, -32700, "parse error"))
			continue
		}
		hasID := len(request.ID) > 0 && string(request.ID) != "null"
		result, rpcErr := handleMCPRequest(request, tools)
		if !hasID {
			continue
		}
		if rpcErr != nil {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": rpcErr})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	return scanner.Err()
}

func handleMCPRequest(request mcpRequest, tools []Tool) (any, map[string]any) {
	switch request.Method {
	case "server/discover":
		return map[string]any{
			"resultType":        "complete",
			"supportedVersions": []string{mcp.LatestProtocol},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"serverInfo":        map[string]any{"name": "ax", "version": mcp.Version},
		}, nil
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := params.ProtocolVersion
		if version == "" {
			version = "2025-11-25"
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ax", "version": mcp.Version},
		}, nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		listed := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			listed = append(listed, map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"inputSchema": tool.Parameters,
			})
		}
		return map[string]any{"resultType": "complete", "tools": listed}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		for _, tool := range tools {
			if tool.Name != params.Name {
				continue
			}
			args := params.Arguments
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage(`{}`)
			}
			output := runTool(context.Background(), []Tool{tool}, ToolCall{Name: tool.Name, Arguments: string(args)}, nil)
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": output}},
				"isError": strings.HasPrefix(output, "error:"),
			}, nil
		}
		return nil, map[string]any{"code": -32602, "message": fmt.Sprintf("unknown tool %q", params.Name)}
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	default:
		return nil, map[string]any{"code": -32601, "message": "method not supported by ax: " + request.Method}
	}
}

func rpcError(id json.RawMessage, code int, message string) map[string]any {
	response := map[string]any{
		"jsonrpc": "2.0",
		"error":   map[string]any{"code": code, "message": message},
	}
	if len(id) > 0 {
		response["id"] = id
	} else {
		response["id"] = nil
	}
	return response
}
