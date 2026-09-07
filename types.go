package main

import (
	"encoding/json"
	"errors"
	"sync/atomic"
)

type ToolCall struct {
	ID        string `json:"ID"`
	Name      string `json:"Name"`
	Arguments string `json:"Arguments"`
}

type Message struct {
	Role       string     `json:"Role"`
	Content    string     `json:"Content"`
	ToolCalls  []ToolCall `json:"ToolCalls,omitempty"`
	ToolCallID string     `json:"ToolCallID,omitempty"`
}

type Usage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CachedInput int `json:"cached_input"`
}

type Event struct {
	Turn    int
	Message Message
	Usage   Usage
}

type runSink interface {
	assistantDelta(text string)
	assistantDone()
	toolStart(call ToolCall)
	toolDelta(call ToolCall, text string)
	toolResult(call ToolCall)
	tokens(input, output, cached int)
	shouldCompact(input, output int) bool
	assistant(turn int, msg Message, usage Usage)
	tool(turn int, msg Message)
	flush()
}

type runOptions struct {
	model    string
	system   string
	tools    []Tool
	maxTurns int
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
	Run         func(arguments string, progress func(string)) string
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
	Kind  streamEventKind
	Text  string
	Call  *ToolCall
	Usage Usage
}

type streamEventKind int

const (
	eventContent streamEventKind = iota
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
	kind    errorKind
	status  int
	message string
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
