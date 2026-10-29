package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const LatestProtocol = "2026-07-28"

const LegacyProtocol = "2025-11-25"

var legacyProtocols = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type TokenSource interface {
	Token(ctx context.Context, force bool) (string, error)
}

type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     []string
	Dir     string
	URL     string
	Headers map[string]string
	Auth    TokenSource
	Timeout time.Duration
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint bool   `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   bool   `json:"openWorldHint,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations *Annotations    `json:"annotations,omitempty"`
}

type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}

type Result struct {
	Content           []Content       `json:"content,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

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
