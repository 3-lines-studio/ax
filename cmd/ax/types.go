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

type ContentPart struct {
	Type  string    `json:"Type"`
	Text  string    `json:"Text,omitempty"`
	Image *ImageRef `json:"Image,omitempty"`
}

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
	steer(msg Message)
	secretRequest(call ToolCall, ask secretAsk)
}

type runOptions struct {
	model    string
	system   string
	tools    []Tool
	maxTurns int

	stable   string
	summary  string
	volatile string

	compactAt       float64
	contextWindow   int
	estimateContext int
	compact         func(ctx context.Context, history []Message, midRun bool) ([]Message, string, error)

	steer     <-chan string
	strategy  kvcacheStrategy
	confirmed []Message
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
	Timeout     int
	Run         func(arguments string, progress func(string)) string
	RunContext  func(ctx context.Context, arguments string, progress func(string)) string
	Validate    func(arguments string) error
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
