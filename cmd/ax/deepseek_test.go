package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func decodeRequestBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestDeepSeekRequestShape(t *testing.T) {
	enabled := true
	o := NewOpenAI("https://api.deepseek.com", "sk-key")
	o.Thinking = &enabled
	o.Effort = "max"
	o.MaxTokens = 131072
	req := &Request{
		Model:  "deepseek-flash",
		System: "sys",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "calling", ReasoningContent: "think", ToolCalls: []ToolCall{{ID: "c1", Name: "bash", Arguments: "{}"}}},
			{Role: "tool", Content: "out", ToolCallID: "c1"},
		},
		Tools: []Tool{{Name: "bash", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	built, err := o.buildRequest(req, true)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeRequestBody(t, built.body)
	if body["thinking"].(map[string]any)["type"] != "enabled" {
		t.Errorf("thinking = %v", body["thinking"])
	}
	if body["reasoning_effort"] != "max" {
		t.Errorf("effort = %v", body["reasoning_effort"])
	}
	if body["max_tokens"].(float64) != 131072 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
	options := body["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Errorf("stream_options = %v", options)
	}
	messages := body["messages"].([]any)
	assistant := messages[2].(map[string]any)
	if assistant["reasoning_content"] != "think" {
		t.Errorf("reasoning_content = %v", assistant["reasoning_content"])
	}
	if len(assistant["tool_calls"].([]any)) != 1 {
		t.Errorf("tool_calls = %v", assistant["tool_calls"])
	}
}

func TestDeepSeekLegacyAssistantGetsEmptyReasoning(t *testing.T) {
	o := NewOpenAI("https://api.deepseek.com", "k")
	req := &Request{
		Model: "deepseek-flash",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "bash", Arguments: "{}"}}},
			{Role: "tool", Content: "out", ToolCallID: "c1"},
		},
		Tools: []Tool{{Name: "bash", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	built, err := o.buildRequest(req, true)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeRequestBody(t, built.body)
	assistant := body["messages"].([]any)[1].(map[string]any)
	value, ok := assistant["reasoning_content"]
	if !ok || value != "" {
		t.Errorf("reasoning_content = %v present=%v", value, ok)
	}
}

func TestDeepSeekThinkingDisabled(t *testing.T) {
	disabled := false
	o := NewOpenAI("https://api.deepseek.com", "k")
	o.Thinking = &disabled
	built, err := o.buildRequest(&Request{Model: "deepseek-flash", Messages: []Message{{Role: "user", Content: "hi"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeRequestBody(t, built.body)
	if body["thinking"].(map[string]any)["type"] != "disabled" {
		t.Errorf("thinking = %v", body["thinking"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort should be omitted when thinking is off: %v", body["reasoning_effort"])
	}
}

func TestOpenAICompatibleOmitsThinking(t *testing.T) {
	o := NewOpenAI("https://example.com/v1", "k")
	built, err := o.buildRequest(&Request{Model: "gpt-4", Messages: []Message{{Role: "user", Content: "hi"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeRequestBody(t, built.body)
	if _, ok := body["thinking"]; ok {
		t.Errorf("thinking leaked to a non-DeepSeek endpoint: %v", body["thinking"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Errorf("effort leaked: %v", body["reasoning_effort"])
	}
}

func TestDeepSeekStreamReasoningAndCacheUsage(t *testing.T) {
	body := "" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"think \"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3,\"prompt_cache_hit_tokens\":7,\"prompt_cache_miss_tokens\":3,\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL, "k")
	handle := o.stream(&Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, &atomic.Bool{})
	var reasoning strings.Builder
	var content strings.Builder
	for ev := range handle.Events() {
		switch ev.Kind {
		case eventReasoning:
			reasoning.WriteString(ev.Reasoning)
		case eventContent:
			content.WriteString(ev.Text)
		}
	}
	resp, err := handle.Join()
	if err != nil {
		t.Fatal(err)
	}
	if reasoning.String() != "think " || content.String() != "answer" {
		t.Errorf("reasoning=%q content=%q", reasoning.String(), content.String())
	}
	if resp.Message.ReasoningContent != "think " {
		t.Errorf("message reasoning = %q", resp.Message.ReasoningContent)
	}
	if resp.Usage.CachedInput != 7 || resp.Usage.Reasoning != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestDeepSeekRetryAfterHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	_, err := o.complete(&Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	var axErr *axError
	if !errors.As(err, &axErr) {
		t.Fatalf("err type = %T", err)
	}
	if axErr.retryAfter == 0 {
		t.Errorf("retryAfter not parsed: %+v", axErr)
	}
}
