package catalog

import (
	"context"
	"encoding/json"
)

type Annotations struct {
	ReadOnly    bool `json:"read_only,omitempty"`
	Destructive bool `json:"destructive,omitempty"`
	Idempotent  bool `json:"idempotent,omitempty"`
	OpenWorld   bool `json:"open_world,omitempty"`
	Sequential  bool `json:"sequential,omitempty"`
}

type Tool struct {
	ID          string          `json:"id"`
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	Annotations Annotations     `json:"annotations,omitempty"`
}

type Result struct {
	Text    string
	IsError bool
}

type Source interface {
	Name() string
	Description() string
	List(ctx context.Context) ([]Tool, error)
	Call(ctx context.Context, name string, args json.RawMessage) (Result, error)
	Close() error
}

type SearchRequest struct {
	Query  string
	Server string
	Tool   string
	Limit  int
}
