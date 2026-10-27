// Package mcp is the small MCP client ax needs: discover, list tools, call a
// tool, over stdio or Streamable HTTP. It implements the subset of protocol
// version 2026-07-28 that the engine uses and falls back to the initialize
// handshake for older servers.
//
// It deliberately does not implement sampling, elicitation, resources,
// prompts, subscriptions or OAuth. The engine exposes a catalog and runs
// tools; servers that need interactive flows get a clear protocol error.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LatestProtocol is the newest protocol revision this client speaks.
const LatestProtocol = "2026-07-28"

// LegacyProtocol is the version offered when a server predates
// server/discover.
const LegacyProtocol = "2025-11-25"

var legacyProtocols = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// TokenSource supplies an OAuth access token for an HTTP server. force asks
// for a refresh after a 401; without force the source may reuse a cached token.
type TokenSource interface {
	Token(ctx context.Context, force bool) (string, error)
}

// ServerConfig describes one configured MCP server.
type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     []string // KEY=VALUE, already resolved by the caller
	Dir     string
	URL     string
	Headers map[string]string
	Auth    TokenSource
	Timeout time.Duration
}

// ServerInfo identifies a server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Annotations mirrors the MCP tool annotation hints.
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint bool   `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   bool   `json:"openWorldHint,omitempty"`
}

// Tool is one tool advertised by a server.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations *Annotations    `json:"annotations,omitempty"`
}

// Content is one content block of a tool result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Result is a tools/call result. IsError is a tool-level error, not a
// protocol failure.
type Result struct {
	Content           []Content       `json:"content,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// Text flattens the result to text the model can read. Non-text content is
// described rather than embedded; ax stores files itself when it matters.
func (r Result) Text() string {
	var parts []string
	for _, c := range r.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		case "image", "audio":
			kind := c.Type
			if c.MimeType != "" {
				kind += " " + c.MimeType
			}
			if c.Data != "" {
				kind += fmt.Sprintf(", %d base64 bytes", len(c.Data))
			}
			parts = append(parts, "["+kind+"]")
		case "resource_link":
			label := "resource"
			if c.Name != "" {
				label += " " + c.Name
			}
			if c.URI != "" {
				label += " " + c.URI
			}
			parts = append(parts, "["+label+"]")
		case "resource":
			if c.Text != "" {
				parts = append(parts, c.Text)
			} else {
				parts = append(parts, "[embedded resource]")
			}
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) > 0 {
		parts = append(parts, string(r.StructuredContent))
	}
	return strings.Join(parts, "\n")
}

// RPCError is a JSON-RPC error returned by a server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("mcp: %s (code %d): %s", e.Message, e.Code, strings.TrimSpace(string(e.Data)))
	}
	return fmt.Sprintf("mcp: %s (code %d)", e.Message, e.Code)
}
