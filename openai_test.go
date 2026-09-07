package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildRequestSerialization(t *testing.T) {
	o := NewOpenAI("http://example.com", "sk-key")
	req := &Request{
		Model:  "gpt-4",
		System: "sys",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "tool", Content: "output", ToolCallID: "c1"},
		},
		Tools: []Tool{{Name: "bash", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	built, err := o.buildRequest(req, true)
	if err != nil {
		t.Fatal(err)
	}
	if built.url != "http://example.com/chat/completions" {
		t.Errorf("url = %q", built.url)
	}
	if len(built.headers) != 2 || built.headers[0] != [2]string{"Content-Type", "application/json"} || built.headers[1] != [2]string{"Authorization", "Bearer sk-key"} {
		t.Errorf("headers = %v", built.headers)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(built.body, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-4" || body["stream"] != true {
		t.Errorf("body = %v", body)
	}
	msgs := body["messages"].([]interface{})
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	first := msgs[0].(map[string]interface{})
	if first["role"] != "system" || first["content"] != "sys" {
		t.Errorf("first message = %v", first)
	}
	last := msgs[2].(map[string]interface{})
	if last["role"] != "tool" || last["tool_call_id"] != "c1" {
		t.Errorf("tool message = %v", last)
	}
	tools := body["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	toolFn := tools[0].(map[string]interface{})["function"].(map[string]interface{})
	if toolFn["name"] != "bash" {
		t.Errorf("tool fn = %v", toolFn)
	}
}

func TestOpenAICompleteSuccess(t *testing.T) {
	var gotAuth string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &gotBody)
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`)
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL, "sk-key")
	resp, err := o.complete(&Request{Model: "gpt-4", System: "sys", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi there" || resp.StopReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.Input != 10 || resp.Usage.Output != 5 || resp.Usage.CachedInput != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if gotAuth != "Bearer sk-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["model"] != "gpt-4" || gotBody["stream"] != false {
		t.Errorf("body = %v", gotBody)
	}
}

func TestOpenAICompleteToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_7","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	resp, err := o.complete(&Request{Model: "gpt-4", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_7" || call.Name != "read" || call.Arguments != `{"path":"a.txt"}` {
		t.Errorf("call = %+v", call)
	}
}

func TestOpenAIStreamContent(t *testing.T) {
	body := "" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL, "k")
	handle := o.stream(&Request{Model: "gpt-4", Messages: []Message{{Role: "user", Content: "hi"}}}, &atomic.Bool{})
	var content strings.Builder
	var lastTokens Usage
	sawDone := false
	for ev := range handle.Events() {
		switch ev.Kind {
		case eventContent:
			content.WriteString(ev.Text)
		case eventTokens:
			lastTokens = ev.Usage
		case eventDone:
			sawDone = true
		}
	}
	resp, err := handle.Join()
	if err != nil {
		t.Fatal(err)
	}
	if !sawDone {
		t.Error("no done event")
	}
	if content.String() != "Hello world" {
		t.Errorf("content = %q", content.String())
	}
	if resp.Message.Content != "Hello world" || resp.StopReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.Input != 10 || resp.Usage.Output != 3 || resp.Usage.CachedInput != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if lastTokens != (Usage{10, 3, 2}) {
		t.Errorf("last tokens = %+v", lastTokens)
	}
}

func TestOpenAIStreamToolCallAccumulation(t *testing.T) {
	body := "" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pwd\"}"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL, "k")
	handle := o.stream(&Request{Model: "gpt-4", Messages: []Message{{Role: "user", Content: "hi"}}}, &atomic.Bool{})
	var calls []ToolCall
	for ev := range handle.Events() {
		if ev.Kind == eventToolCall && ev.Call != nil {
			calls = append(calls, *ev.Call)
		}
	}
	resp, err := handle.Join()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "bash" || calls[0].Arguments != `{"command":"pwd"}` {
		t.Errorf("call = %+v", calls[0])
	}
	if resp.StopReason != "tool_calls" {
		t.Errorf("stop reason = %q", resp.StopReason)
	}
}

func TestOpenAIHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	_, err := o.complete(&Request{Model: "gpt-4", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *axError
	if !errors.As(err, &ae) || ae.kind != errHTTP || ae.status != 429 {
		t.Errorf("err = %v kind=%v status=%d", err, ae.kind, ae.status)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenAIUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "oops")
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	_, err := o.complete(&Request{Model: "gpt-4"})
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenAINoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	_, err := o.complete(&Request{Model: "gpt-4"})
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenAIInvalidJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "garbage")
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	_, err := o.complete(&Request{Model: "gpt-4"})
	if err == nil || !strings.Contains(err.Error(), "openai:") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenAIStreamInvalidBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not an sse stream"))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	handle := o.stream(&Request{Model: "gpt-4"}, &atomic.Bool{})
	for ev := range handle.Events() {
		_ = ev
	}
	_, err := handle.Join()
	if err == nil || !strings.Contains(err.Error(), "invalid response body") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenAITransportError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	o := NewOpenAI("http://"+addr, "k")
	_, err = o.complete(&Request{Model: "gpt-4"})
	if err == nil {
		t.Fatal("expected transport error")
	}
	var ae *axError
	if !errors.As(err, &ae) || ae.kind != errTransport {
		t.Errorf("err = %v kind=%v", err, ae.kind)
	}
}

func TestOpenAIStreamCancelContext(t *testing.T) {
	blocker := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocker
	}))
	defer func() { close(blocker); srv.Close() }()

	o := NewOpenAI(srv.URL, "k")
	cancel := &atomic.Bool{}
	cancel.Store(true)
	handle := o.stream(&Request{Model: "gpt-4"}, cancel)

	var gotErr error
	done := make(chan struct{})
	go func() {
		_, gotErr = handle.Join()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Join hung")
	}
	if gotErr == nil {
		t.Fatal("expected interruption error")
	}
	var ae *axError
	if !errors.As(gotErr, &ae) || ae.message != "interrupted" {
		t.Errorf("err = %v", gotErr)
	}
}
