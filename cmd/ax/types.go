package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"
)

type ToolCall struct {
	ID        string `json:"ID"`
	Name      string `json:"Name"`
	Arguments string `json:"Arguments"`
}

// ContentPart is one block of a multimodal message. Text parts carry Text;
// image parts carry an Image reference. When Parts is non-empty it is the
// authoritative content; Content is kept as the plain-text rendering so older
// readers and logs stay useful.
type ContentPart struct {
	Type  string    `json:"Type"`
	Text  string    `json:"Text,omitempty"`
	Image *ImageRef `json:"Image,omitempty"`
}

// ImageRef points at an image without embedding it in the session log. A
// Blob is a sha256 in the session's blob directory; a URL is either an
// http(s) link or a data: URL. Exactly one of Blob and URL is set.
type ImageRef struct {
	URL       string `json:"URL,omitempty"`
	Blob      string `json:"Blob,omitempty"`
	MediaType string `json:"MediaType,omitempty"`
	Detail    string `json:"Detail,omitempty"`
	Name      string `json:"Name,omitempty"`
}

type Message struct {
	Role             string        `json:"Role"`
	Content          string        `json:"Content"`
	ContentParts     []ContentPart `json:"ContentParts,omitempty"`
	ReasoningContent string        `json:"ReasoningContent,omitempty"`
	ToolCalls        []ToolCall    `json:"ToolCalls,omitempty"`
	ToolCallID       string        `json:"ToolCallID,omitempty"`
}

// Text returns the plain-text content of the message, including text parts.
func (m Message) Text() string {
	if len(m.ContentParts) == 0 {
		return m.Content
	}
	var out strings.Builder
	for _, part := range m.ContentParts {
		if part.Type == "text" {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

// HasImages reports whether the message carries image parts.
func (m Message) HasImages() bool {
	for _, part := range m.ContentParts {
		if part.Type == "image" && part.Image != nil {
			return true
		}
	}
	return false
}

type Usage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CachedInput int `json:"cached_input"`
	Reasoning   int `json:"reasoning,omitempty"`
}

type Event struct {
	Turn    int
	Message Message
	Usage   Usage
}

type runSink interface {
	assistantDelta(text string)
	reasoningDelta(text string)
	assistantDone()
	toolStart(call ToolCall)
	toolDelta(call ToolCall, text string)
	toolResult(call ToolCall)
	tokens(input, output, cached int)
	assistant(turn int, msg Message, usage Usage)
	tool(turn int, msg Message)
	// steer records a user message that arrived while the run was going.
	steer(msg Message)
	// secretRequest announces that a tool needs a value only the host can give.
	secretRequest(call ToolCall, ask secretAsk)
}

type runOptions struct {
	model    string
	system   string
	tools    []Tool
	maxTurns int

	// Context compaction. compactAt is a fraction of contextWindow; zero
	// disables it. compact rewrites the history and the system prompt.
	compactAt     float64
	contextWindow int
	compact       func(ctx context.Context, history []Message, midRun bool) ([]Message, string, error)

	// steer carries messages typed while the run is already going. The loop
	// drains it at each turn boundary: after the tools of a batch finish and
	// before the next model call, never in the middle of either.
	steer <-chan string
}

type outcome int

const (
	outcomeDone outcome = iota
	outcomeCancelled
	outcomeMaxTurns
	outcomeCompact
	outcomeFailed
)

type runEnd struct {
	messages []Message
	usage    Usage
	outcome  outcome
	failMsg  string
}

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Snippet     string
	Sequential  bool
	Replay      string
	ReadOnly    bool
	Destructive bool
	OpenWorld   bool
	Timeout     int // seconds; 0 means the engine default
	// Run is the legacy in-process entry point. External providers use
	// RunContext so the engine can cancel or time out the process.
	Run        func(arguments string, progress func(string)) string
	RunContext func(ctx context.Context, arguments string, progress func(string)) string
	Validate   func(arguments string) error
}

type Provider interface {
	stream(req *Request, cancel *atomic.Bool) *StreamHandle
}

type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []Tool
}

type Response struct {
	Message    Message
	Usage      Usage
	StopReason string
}

type StreamEvent struct {
	Kind      streamEventKind
	Text      string
	Call      *ToolCall
	Usage     Usage
	Reasoning string
}

type streamEventKind int

const (
	eventContent streamEventKind = iota
	eventReasoning
	eventToolCall
	eventTokens
	eventDone
)

type StreamHandle struct {
	events chan StreamEvent
	join   func() (Response, error)
}

func (h *StreamHandle) Events() <-chan StreamEvent { return h.events }
func (h *StreamHandle) Join() (Response, error)    { return h.join() }

type errorKind int

const (
	errTransport errorKind = iota
	errProvider
	errHTTP
)

type axError struct {
	kind       errorKind
	status     int
	message    string
	retryAfter time.Duration
}

func (e *axError) Error() string { return e.message }

func newTransport(message string) error { return &axError{kind: errTransport, message: message} }
func newProvider(message string) error  { return &axError{kind: errProvider, message: message} }
func newHTTP(status int, message string) error {
	return &axError{kind: errHTTP, status: status, message: message}
}

func retryable(e error) bool {
	var ae *axError
	if !errors.As(e, &ae) {
		return false
	}
	switch ae.kind {
	case errTransport:
		return true
	case errHTTP:
		return ae.status == 408 || ae.status == 409 || ae.status == 429 || ae.status >= 500
	}
	return false
}
