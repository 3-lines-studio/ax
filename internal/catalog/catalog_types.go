// Package catalog turns configured tool sources into the index behind ax's
// search and execute tools. A source is either a local describe/run provider
// or an MCP server; the catalog only knows names, descriptions, schemas and
// annotations, and how to call one tool.
package catalog

import (
	"context"
	"encoding/json"
)

// Annotations describe side effects. Local providers derive them from their
// descriptor; MCP servers provide the standard hints.
type Annotations struct {
	ReadOnly    bool `json:"read_only,omitempty"`
	Destructive bool `json:"destructive,omitempty"`
	Idempotent  bool `json:"idempotent,omitempty"`
	OpenWorld   bool `json:"open_world,omitempty"`
	Sequential  bool `json:"sequential,omitempty"`
}

// Tool is one catalog entry.
type Tool struct {
	ID          string          `json:"id"`
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	Annotations Annotations     `json:"annotations,omitempty"`
}

// Result is what a tool returns to the model.
type Result struct {
	Text    string
	IsError bool
}

// Source is one place tools come from.
type Source interface {
	Name() string
	Description() string
	List(ctx context.Context) ([]Tool, error)
	Call(ctx context.Context, name string, args json.RawMessage) (Result, error)
	Close() error
}

// SearchRequest is the input of the search tool.
type SearchRequest struct {
	Query  string
	Server string
	Tool   string
	Limit  int
}
