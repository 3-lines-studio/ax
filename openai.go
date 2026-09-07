package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const maxStreamToolCalls = 64

type OpenAI struct {
	BaseURL string
	APIKey  string
}

func NewOpenAI(baseURL, apiKey string) OpenAI {
	return OpenAI{BaseURL: baseURL, APIKey: apiKey}
}

type builtRequest struct {
	url     string
	headers [][2]string
	body    []byte
}

type resultPair struct {
	resp Response
	err  error
}

func (o OpenAI) buildRequest(req *Request, stream bool) (builtRequest, error) {
	var msgs []oaMessage
	if req.System != "" {
		system := req.System
		msgs = append(msgs, oaMessage{Role: "system", Content: &system})
	}
	for _, m := range req.Messages {
		var toolCalls []oaToolCall
		if len(m.ToolCalls) > 0 {
			for _, c := range m.ToolCalls {
				toolCalls = append(toolCalls, oaToolCall{
					ID: c.ID, Type: "function",
					Function: oaFunction{Name: c.Name, Arguments: c.Arguments},
				})
			}
		}
		var content *string
		if m.Content != "" || m.Role == "tool" {
			c := m.Content
			content = &c
		}
		var toolCallID *string
		if m.ToolCallID != "" {
			tc := m.ToolCallID
			toolCallID = &tc
		}
		msgs = append(msgs, oaMessage{
			Role: m.Role, Content: content, ToolCalls: toolCalls, ToolCallID: toolCallID,
		})
	}
	var tools []oaTool
	for _, t := range req.Tools {
		tools = append(tools, oaTool{Type: "function", Function: oaToolFunction{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		}})
	}
	body, err := json.Marshal(oaRequest{Model: req.Model, Messages: msgs, Tools: tools, Stream: stream})
	if err != nil {
		return builtRequest{}, newProvider(err.Error())
	}
	return builtRequest{
		url:     o.BaseURL + "/chat/completions",
		headers: [][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + o.APIKey}},
		body:    body,
	}, nil
}

func (o OpenAI) stream(req *Request, cancel *atomic.Bool) *StreamHandle {
	built, err := o.buildRequest(req, true)
	events := make(chan StreamEvent, 256)
	result := make(chan resultPair, 1)
	if err != nil {
		result <- resultPair{err: err}
		close(events)
		return &StreamHandle{events: events, join: func() (Response, error) {
			p := <-result
			return p.resp, p.err
		}}
	}
	go func() {
		resp, rErr := o.runRequest(built, cancel, events)
		result <- resultPair{resp: resp, err: rErr}
		close(events)
	}()
	return &StreamHandle{events: events, join: func() (Response, error) {
		p := <-result
		return p.resp, p.err
	}}
}

func (o OpenAI) complete(req *Request) (Response, error) {
	built, err := o.buildRequest(req, false)
	if err != nil {
		return Response{}, err
	}
	return o.runRequest(built, &atomic.Bool{}, nil)
}

func (o OpenAI) runRequest(built builtRequest, cancel *atomic.Bool, events chan StreamEvent) (Response, error) {
	httpReq, err := http.NewRequest(http.MethodPost, built.url, bytes.NewReader(built.body))
	if err != nil {
		return Response{}, newTransport(err.Error())
	}
	for _, h := range built.headers {
		httpReq.Header.Set(h[0], h[1])
	}
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	httpReq = httpReq.WithContext(ctx)
	if cancel != nil {
		go watchCancel(ctx, cancelCtx, cancel)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		if cancel != nil && cancel.Load() {
			return Response{}, newProvider("interrupted")
		}
		return Response{}, newTransport(err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		message := ""
		var e oaError
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			message = e.Error.Message
		}
		if message != "" {
			return Response{}, newHTTP(resp.StatusCode, fmt.Sprintf("openai: %d: %s", resp.StatusCode, message))
		}
		return Response{}, newHTTP(resp.StatusCode, fmt.Sprintf("openai: unexpected status %d", resp.StatusCode))
	}

	acc := &streamAcc{}
	if events != nil {
		if err := acc.feedReader(resp.Body, events); err != nil {
			if cancel != nil && cancel.Load() {
				return Response{}, newProvider("interrupted")
			}
			var mse malformedStreamError
			if errors.As(err, &mse) {
				return Response{}, newProvider(mse.Error())
			}
			return Response{}, newTransport(err.Error())
		}
		if acc.events == 0 {
			snippet := acc.raw
			if len(snippet) > 200 {
				snippet = snippet[:200]
			}
			return Response{}, newProvider(fmt.Sprintf("openai: invalid response body: %s", string(snippet)))
		}
		acc.finish(events)
		return acc.response(), nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed oaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, newProvider(fmt.Sprintf("openai: %s", err))
	}
	if len(parsed.Choices) == 0 {
		return Response{}, newProvider("openai: no choices in response")
	}
	choice := parsed.Choices[0]
	stopReason := ""
	if choice.FinishReason != nil {
		stopReason = *choice.FinishReason
	}
	var calls []ToolCall
	for _, c := range choice.Message.ToolCalls {
		calls = append(calls, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	content := ""
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}
	return Response{
		Message: Message{Role: "assistant", Content: content, ToolCalls: calls},
		Usage: Usage{
			Input:       parsed.Usage.PromptTokens,
			Output:      parsed.Usage.CompletionTokens,
			CachedInput: parsed.Usage.PromptTokensDetails.CachedTokens,
		},
		StopReason: stopReason,
	}, nil
}

func watchCancel(ctx context.Context, cancelCtx context.CancelFunc, cancel *atomic.Bool) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cancel.Load() {
				cancelCtx()
				return
			}
		}
	}
}

// malformedStreamError means the provider sent a stream chunk that could not
// be decoded. It is provider-data corruption, not a transport failure, so the
// run fails rather than silently dropping the chunk.
type malformedStreamError struct{ err error }

func (e malformedStreamError) Error() string {
	return "openai: malformed stream chunk: " + e.err.Error()
}

type streamAcc struct {
	raw          []byte
	buf          []byte
	content      string
	calls        []oaToolCallDelta
	usage        oaUsage
	outTokens    int
	finishReason string
	events       int
}

func (acc *streamAcc) feedReader(r io.Reader, tx chan StreamEvent) error {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if err := acc.feed(buf[:n], tx); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (acc *streamAcc) feed(data []byte, tx chan StreamEvent) error {
	acc.raw = append(acc.raw, data...)
	if bytes.Contains(data, []byte{'\r'}) || (len(acc.buf) > 0 && acc.buf[len(acc.buf)-1] == '\r') {
		prevCR := len(acc.buf) > 0 && acc.buf[len(acc.buf)-1] == '\r'
		for _, b := range data {
			if b == '\n' && prevCR {
				acc.buf = acc.buf[:len(acc.buf)-1]
			}
			acc.buf = append(acc.buf, b)
			prevCR = b == '\r'
		}
	} else {
		acc.buf = append(acc.buf, data...)
	}
	for {
		sep := bytes.Index(acc.buf, []byte("\n\n"))
		if sep < 0 {
			break
		}
		event := acc.buf[:sep+2]
		acc.buf = acc.buf[sep+2:]
		if err := acc.handleEvent(event, tx); err != nil {
			return err
		}
	}
	return nil
}

func (acc *streamAcc) handleEvent(event []byte, tx chan StreamEvent) error {
	var payload strings.Builder
	hasData := false
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			if hasData {
				payload.WriteByte('\n')
			}
			payload.WriteString(strings.TrimSpace(string(line[len("data:"):])))
			hasData = true
		}
	}
	if !hasData {
		return nil
	}
	acc.events++
	text := payload.String()
	if text == "[DONE]" {
		return nil
	}
	var chunk oaStreamChunk
	if err := json.Unmarshal([]byte(text), &chunk); err != nil {
		return malformedStreamError{err: fmt.Errorf("%w (after %d events)", err, acc.events)}
	}
	if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
		acc.usage = chunk.Usage
		if tx != nil {
			tx <- StreamEvent{Kind: eventTokens, Usage: Usage{
				Input: acc.usage.PromptTokens, Output: acc.usage.CompletionTokens,
				CachedInput: acc.usage.PromptTokensDetails.CachedTokens,
			}}
		}
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		acc.finishReason = *choice.FinishReason
	}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		acc.content += *choice.Delta.Content
		acc.outTokens++
		if tx != nil {
			tx <- StreamEvent{Kind: eventContent, Text: *choice.Delta.Content}
			tx <- StreamEvent{Kind: eventTokens, Usage: Usage{
				Input: acc.usage.PromptTokens, Output: acc.outTokens,
				CachedInput: acc.usage.PromptTokensDetails.CachedTokens,
			}}
		}
	}
	if choice.Delta.ToolCalls != nil {
		for _, call := range choice.Delta.ToolCalls {
			if call.Index >= maxStreamToolCalls {
				continue
			}
			idx := call.Index
			for len(acc.calls) <= idx {
				acc.calls = append(acc.calls, oaToolCallDelta{})
			}
			entry := &acc.calls[idx]
			if call.ID != nil {
				entry.ID = call.ID
			}
			if call.Function != nil {
				if entry.Function == nil {
					entry.Function = &oaFunctionDelta{}
				}
				if call.Function.Name != nil {
					entry.Function.Name = call.Function.Name
				}
				if call.Function.Arguments != nil {
					if entry.Function.Arguments == nil {
						entry.Function.Arguments = call.Function.Arguments
					} else {
						s := *entry.Function.Arguments + *call.Function.Arguments
						entry.Function.Arguments = &s
					}
				}
			}
		}
	}
	return nil
}

func (acc *streamAcc) toolCalls() []ToolCall {
	var out []ToolCall
	for _, call := range acc.calls {
		name := ""
		arguments := ""
		if call.Function != nil {
			if call.Function.Name != nil {
				name = *call.Function.Name
			}
			if call.Function.Arguments != nil {
				arguments = *call.Function.Arguments
			}
		}
		id := ""
		if call.ID != nil {
			id = *call.ID
		}
		out = append(out, ToolCall{ID: id, Name: name, Arguments: arguments})
	}
	return out
}

func (acc *streamAcc) finish(tx chan StreamEvent) {
	for _, call := range acc.toolCalls() {
		c := call
		tx <- StreamEvent{Kind: eventToolCall, Call: &c}
	}
	tx <- StreamEvent{Kind: eventDone}
}

func (acc *streamAcc) response() Response {
	return Response{
		Message: Message{Role: "assistant", Content: acc.content, ToolCalls: acc.toolCalls()},
		Usage: Usage{
			Input:       acc.usage.PromptTokens,
			Output:      acc.usage.CompletionTokens,
			CachedInput: acc.usage.PromptTokensDetails.CachedTokens,
		},
		StopReason: acc.finishReason,
	}
}

type oaRequest struct {
	Model    string      `json:"model"`
	Messages []oaMessage `json:"messages"`
	Tools    []oaTool    `json:"tools,omitempty"`
	Stream   bool        `json:"stream"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string      `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Function oaFunction `json:"function"`
}

type oaFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaTool struct {
	Type     string         `json:"type"`
	Function oaToolFunction `json:"function"`
}

type oaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaResponse struct {
	Choices []oaChoice `json:"choices,omitempty"`
	Usage   oaUsage    `json:"usage,omitempty"`
}

type oaChoice struct {
	Message      oaMessage `json:"message"`
	FinishReason *string   `json:"finish_reason,omitempty"`
}

type oaErrorPayload struct {
	Message string `json:"message"`
}

type oaError struct {
	Error oaErrorPayload `json:"error"`
}

type oaUsage struct {
	PromptTokens        int                `json:"prompt_tokens,omitempty"`
	CompletionTokens    int                `json:"completion_tokens,omitempty"`
	PromptTokensDetails promptTokensDetail `json:"prompt_tokens_details"`
}

type promptTokensDetail struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type oaStreamChunk struct {
	Choices []oaStreamChoice `json:"choices,omitempty"`
	Usage   oaUsage          `json:"usage,omitempty"`
}

type oaStreamChoice struct {
	Delta        oaDeltaMessage `json:"delta"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

type oaDeltaMessage struct {
	Content   *string           `json:"content,omitempty"`
	ToolCalls []oaToolCallDelta `json:"tool_calls,omitempty"`
}

type oaToolCallDelta struct {
	Index    int              `json:"index"`
	ID       *string          `json:"id,omitempty"`
	Function *oaFunctionDelta `json:"function,omitempty"`
}

type oaFunctionDelta struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}
