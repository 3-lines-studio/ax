package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/3-lines-studio/ax/internal/catalog"
)

const searchSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "What you want to do, in natural language."},
    "server": {"type": "string", "description": "Only look in this server."},
    "tool": {"type": "string", "description": "Exact tool id to get its full schema and an example call."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  }
}`

const executeSchema = `{
  "type": "object",
  "properties": {
    "tool": {"type": "string", "description": "Tool id from search, e.g. fsx.read or github.create_issue."},
    "arguments": {
      "type": ["object", "string"],
      "description": "JSON arguments matching the tool schema. A JSON-encoded string is accepted too."
    }
  },
  "required": ["tool", "arguments"]
}`

const maxSearchOutput = 24 * 1024

func catalogTools(cat *catalog.Catalog) []Tool {
	searchSchemaCompiled, err := compileSchema([]byte(searchSchema))
	if err != nil {
		panic("search schema: " + err.Error())
	}
	executeSchemaCompiled, err := compileSchema([]byte(executeSchema))
	if err != nil {
		panic("execute schema: " + err.Error())
	}
	return []Tool{
		{
			Name:        "search",
			Description: "Find tools. Call with no arguments for the whole catalog; query ranks matches; server lists that server's tools; tool returns the full schema. Never returns an empty result.",
			Snippet:     "Search the tool catalog",
			Parameters:  []byte(searchSchema),
			Sequential:  false,
			Replay:      "safe",
			ReadOnly:    true,
			Timeout:     60,
			Validate:    searchSchemaCompiled.validate,
			RunContext: func(_ context.Context, arguments string, _ func(string)) string {
				var request struct {
					Query  string `json:"query"`
					Server string `json:"server"`
					Tool   string `json:"tool"`
					Limit  int    `json:"limit"`
				}
				if err := json.Unmarshal([]byte(arguments), &request); err != nil {
					return "error: " + err.Error()
				}
				return sanitize(truncateStart(cat.Search(catalog.SearchRequest{
					Query: request.Query, Server: request.Server, Tool: request.Tool, Limit: request.Limit,
				}), maxSearchOutput))
			},
		},
		{
			Name:        "execute",
			Description: "Run one tool from the catalog. Search first if you do not know the exact id. Arguments must match the tool schema.",
			Snippet:     "Run one catalog tool",
			Parameters:  []byte(executeSchema),
			Sequential:  true,
			Timeout:     300,
			Validate:    executeSchemaCompiled.validate,
			RunContext:  executeTool(cat),
		},
	}
}

func executeTool(cat *catalog.Catalog) func(context.Context, string, func(string)) string {
	var mu sync.Mutex
	schemas := map[string]*jsonSchema{}
	compiled := func(tool catalog.Tool) (*jsonSchema, error) {
		mu.Lock()
		defer mu.Unlock()
		if schema, ok := schemas[tool.ID]; ok {
			return schema, nil
		}
		schema, err := compileSchema(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s has an invalid schema: %w", tool.ID, err)
		}
		schemas[tool.ID] = schema
		return schema, nil
	}
	return func(ctx context.Context, arguments string, _ func(string)) string {
		var request struct {
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(arguments), &request); err != nil {
			return "error: invalid execute arguments: " + err.Error()
		}
		tool, err := cat.Resolve(request.Tool)
		if err != nil {
			return "error: " + err.Error()
		}
		args, err := normalizeArguments(request.Arguments)
		if err != nil {
			return "error: " + tool.ID + ": " + err.Error()
		}
		schema, err := compiled(tool)
		if err != nil {
			return "error: " + err.Error()
		}
		if err := schema.validate(string(args)); err != nil {
			return fmt.Sprintf("error: invalid arguments for %s: %v", tool.ID, err)
		}
		result, err := cat.Call(ctx, tool.ID, args)
		if err != nil {
			return "error: " + tool.ID + ": " + err.Error()
		}
		text := strings.TrimSpace(result.Text)
		if text == "" {
			if result.IsError {
				return "error: " + tool.ID + " failed without output"
			}
			return "error: " + tool.ID + " returned no output"
		}
		if result.IsError && !strings.HasPrefix(text, "error:") {
			text = "error: " + text
		}
		return sanitize(tail(text))
	}
}

func normalizeArguments(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return json.RawMessage(`{}`), nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
		trimmed = []byte(strings.TrimSpace(text))
	}
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	return json.RawMessage(trimmed), nil
}

func truncateStart(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8Start(value[cut]) {
		cut--
	}
	return value[:cut] + "\n... (truncated; refine the search)"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
