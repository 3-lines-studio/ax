package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxStreamToolCalls = 64
	maxRequestBody     = 48 << 20 // DeepSeek request body limit
)

type OpenAI struct {
	BaseURL string
	APIKey  string
	// SessionPath resolves image blobs to data URLs at request time.
	SessionPath string
	// Thinking is nil for auto: DeepSeek models think, others do not.
	Thinking  *bool
	Effort    string
	MaxTokens int
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

func (o OpenAI) deepSeek(model string) bool {
	base := strings.ToLower(o.BaseURL)
	return strings.Contains(base, "deepseek") || strings.HasPrefix(strings.ToLower(model), "deepseek")
}

// thinkingFor reports whether thinking is enabled and whether the field should
// be sent at all. Non-DeepSeek providers omit the field unless the caller set
// it explicitly.
func (o OpenAI) thinkingFor(model string) (enabled, explicit bool) {
	if o.Thinking != nil {
		return *o.Thinking, true
	}
	if o.deepSeek(model) {
		return true, true
	}
	return false, false
}

func (o OpenAI) buildRequest(req *Request, stream bool) (builtRequest, error) {
	var msgs []oaMessage
	if req.System != "" {
		system := req.System
		msgs = append(msgs, oaMessage{Role: "system", Content: system})
	}
	hasTools := len(req.Tools) > 0
	for _, m := range req.Messages {
		message := oaMessage{Role: m.Role}
		if len(m.ToolCalls) > 0 {
			for _, c := range m.ToolCalls {
				message.ToolCalls = append(message.ToolCalls, oaToolCall{
					ID: c.ID, Type: "function",
					Function: oaFunction{Name: c.Name, Arguments: c.Arguments},
				})
			}
		}
		if m.Role == "user" && len(m.ContentParts) > 0 {
			parts, err := o.contentParts(m)
			if err != nil {
				return builtRequest{}, err
			}
			message.Content = parts
		} else if m.Content != "" || m.Role == "tool" {
			content := m.Content
			message.Content = content
		}
		if m.ToolCallID != "" {
			tc := m.ToolCallID
			message.ToolCallID = &tc
		}
		// DeepSeek requires reasoning_content on every assistant message when
		// the request carries tools. Legacy sessions did not store it; an
		// empty string keeps those histories structurally valid. Other
		// providers only see the field when there is real reasoning.
		if m.Role == "assistant" && (m.ReasoningContent != "" || (hasTools && o.deepSeek(req.Model))) {
			reasoning := m.ReasoningContent
			message.ReasoningContent = &reasoning
		}
		msgs = append(msgs, message)
	}
	var tools []oaTool
	for _, t := range req.Tools {
		tools = append(tools, oaTool{Type: "function", Function: oaToolFunction{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		}})
	}
	request := oaRequest{Model: req.Model, Messages: msgs, Tools: tools, Stream: stream}
	if stream {
		request.StreamOptions = &oaStreamOptions{IncludeUsage: true}
	}
	enabled, explicit := o.thinkingFor(req.Model)
	// A resumed session written before reasoning was recorded cannot satisfy
	// DeepSeek's thinking-mode rule. In auto mode, drop to non-thinking for
	// that request instead of failing the whole history.
	if enabled && hasTools && o.Thinking == nil {
		for _, message := range req.Messages {
			if message.Role == "assistant" && message.ReasoningContent == "" {
				enabled = false
				break
			}
		}
	}
	if explicit {
		kind := "disabled"
		if enabled {
			kind = "enabled"
			effort := o.Effort
			if effort == "" {
				effort = "max"
			}
			request.ReasoningEffort = effort
		}
		request.Thinking = &oaThinking{Type: kind}
	}
	if o.MaxTokens > 0 {
		request.MaxTokens = o.MaxTokens
	}
	body, err := json.Marshal(request)
	if err != nil {
		return builtRequest{}, newProvider(err.Error())
	}
	if len(body) > maxRequestBody && o.deepSeek(req.Model) {
		return builtRequest{}, newProvider(fmt.Sprintf("request body is %d MiB, over DeepSeek's 48 MiB limit; use image URLs or smaller images", len(body)>>20))
	}
	return builtRequest{
		url:     o.BaseURL + "/chat/completions",
		headers: [][2]string{{"Content-Type", "application/json"}, {"Authorization", "Bearer " + o.APIKey}},
		body:    body,
	}, nil
}

func (o OpenAI) contentParts(m Message) ([]oaContentPart, error) {
	parts := make([]oaContentPart, 0, len(m.ContentParts))
	hasText := false
	for _, part := range m.ContentParts {
		switch part.Type {
		case "text":
			if part.Text == "" {
				continue
			}
			hasText = true
			parts = append(parts, oaContentPart{Type: "text", Text: part.Text})
		case "image":
			if part.Image == nil {
				continue
			}
			url, err := imageURL(*part.Image, o.SessionPath)
			if err != nil {
				return nil, err
			}
			image := &oaImageURL{URL: url, Detail: part.Image.Detail}
			parts = append(parts, oaContentPart{Type: "image_url", ImageURL: image})
		}
	}
	if !hasText && m.Content != "" {
		parts = append([]oaContentPart{{Type: "text", Text: m.Content}}, parts...)
	}
	if len(parts) == 0 {
		return nil, newProvider("message has content parts but no usable text or image")
	}
	return parts, nil
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

// providerClient bounds the wait for response headers. The body can stream for
// as long as the model needs; the context still cancels a run.
var providerClient = &http.Client{Transport: func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 3 * time.Minute
	transport.TLSHandshakeTimeout = 15 * time.Second
	return transport
}()}

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
	resp, err := providerClient.Do(httpReq)
	if err != nil {
		if cancel != nil && cancel.Load() {
			return Response{}, newProvider("interrupted")
		}
		return Response{}, newTransport(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		message := ""
		var e oaError
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			message = e.Error.Message
		}
		var axErr *axError
		if message != "" {
			axErr = &axError{kind: errHTTP, status: resp.StatusCode, message: fmt.Sprintf("openai: %d: %s", resp.StatusCode, message)}
		} else {
			axErr = &axError{kind: errHTTP, status: resp.StatusCode, message: fmt.Sprintf("openai: unexpected status %d", resp.StatusCode)}
		}
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			axErr.retryAfter = time.Duration(seconds) * time.Second
		}
		return Response{}, axErr
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
	content := contentString(choice.Message.Content)
	reasoning := ""
	if choice.Message.ReasoningContent != nil {
		reasoning = *choice.Message.ReasoningContent
	}
	return Response{
		Message:    Message{Role: "assistant", Content: content, ReasoningContent: reasoning, ToolCalls: calls},
		Usage:      parsed.Usage.usage(),
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
	reasoning    string
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
	if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 || chunk.Usage.PromptCacheHitTokens != 0 {
		acc.usage = chunk.Usage
		if tx != nil {
			tx <- StreamEvent{Kind: eventTokens, Usage: acc.usage.usage()}
		}
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		acc.finishReason = *choice.FinishReason
	}
	if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
		acc.reasoning += *choice.Delta.ReasoningContent
		acc.outTokens++
		if tx != nil {
			tx <- StreamEvent{Kind: eventReasoning, Reasoning: *choice.Delta.ReasoningContent}
			tx <- StreamEvent{Kind: eventTokens, Usage: Usage{
				Input: acc.usage.PromptTokens, Output: acc.outTokens,
				CachedInput: acc.usage.cached(),
			}}
		}
	}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		acc.content += *choice.Delta.Content
		acc.outTokens++
		if tx != nil {
			tx <- StreamEvent{Kind: eventContent, Text: *choice.Delta.Content}
			tx <- StreamEvent{Kind: eventTokens, Usage: Usage{
				Input: acc.usage.PromptTokens, Output: acc.outTokens,
				CachedInput: acc.usage.cached(),
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
		Message:    Message{Role: "assistant", Content: acc.content, ReasoningContent: acc.reasoning, ToolCalls: acc.toolCalls()},
		Usage:      acc.usage.usage(),
		StopReason: acc.finishReason,
	}
}

func contentString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		data, _ := json.Marshal(typed)
		return string(data)
	}
}

type oaRequest struct {
	Model           string           `json:"model"`
	Messages        []oaMessage      `json:"messages"`
	Tools           []oaTool         `json:"tools,omitempty"`
	Stream          bool             `json:"stream"`
	StreamOptions   *oaStreamOptions `json:"stream_options,omitempty"`
	Thinking        *oaThinking      `json:"thinking,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	MaxTokens       int              `json:"max_tokens,omitempty"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaThinking struct {
	Type string `json:"type"`
}

type oaMessage struct {
	Role             string       `json:"role"`
	Content          any          `json:"content,omitempty"`
	ReasoningContent *string      `json:"reasoning_content,omitempty"`
	ToolCalls        []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID       *string      `json:"tool_call_id,omitempty"`
}

type oaContentPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
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
	PromptTokens        int `json:"prompt_tokens,omitempty"`
	CompletionTokens    int `json:"completion_tokens,omitempty"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens,omitempty"`
	} `json:"prompt_tokens_details"`
	PromptCacheHitTokens    int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens   int `json:"prompt_cache_miss_tokens,omitempty"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	} `json:"completion_tokens_details"`
}

func (u oaUsage) cached() int {
	if u.PromptTokensDetails.CachedTokens != 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return u.PromptCacheHitTokens
}

func (u oaUsage) usage() Usage {
	return Usage{
		Input:       u.PromptTokens,
		Output:      u.CompletionTokens,
		CachedInput: u.cached(),
		Reasoning:   u.CompletionTokensDetails.ReasoningTokens,
	}
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
	Content          *string           `json:"content,omitempty"`
	ReasoningContent *string           `json:"reasoning_content,omitempty"`
	ToolCalls        []oaToolCallDelta `json:"tool_calls,omitempty"`
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
