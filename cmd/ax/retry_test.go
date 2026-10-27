package main

import (
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
)

type recordingSink struct {
	reasoning string
	deltas    []string
	dones     int
	starts    []ToolCall
	toolDel   []string
	results   []string
	tok       []Usage
	assist    []Message
	tools     []Message
	flushCnt  int
}

func (r *recordingSink) assistantDelta(text string)           { r.deltas = append(r.deltas, text) }
func (r *recordingSink) reasoningDelta(text string)           { r.reasoning += text }
func (r *recordingSink) assistantDone()                       { r.dones++ }
func (r *recordingSink) toolStart(call ToolCall)              { r.starts = append(r.starts, call) }
func (r *recordingSink) toolDelta(call ToolCall, text string) { r.toolDel = append(r.toolDel, text) }
func (r *recordingSink) toolResult(call ToolCall)             { r.results = append(r.results, call.ID) }
func (r *recordingSink) tokens(input, output, cached int) {
	r.tok = append(r.tok, Usage{Input: input, Output: output, CachedInput: cached})
}
func (r *recordingSink) shouldCompact(input, output int) bool      { return false }
func (r *recordingSink) assistant(turn int, msg Message, us Usage) { r.assist = append(r.assist, msg) }
func (r *recordingSink) tool(turn int, msg Message)                { r.tools = append(r.tools, msg) }
func (r *recordingSink) flush()                                    { r.flushCnt++ }

type fakeProvider struct {
	handles []*StreamHandle
	idx     int
}

func (f *fakeProvider) stream(req *Request, cancel *atomic.Bool) *StreamHandle {
	h := f.handles[f.idx]
	f.idx++
	return h
}

func handleWithResponse(resp Response, err error, events ...StreamEvent) *StreamHandle {
	ch := make(chan StreamEvent, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return &StreamHandle{events: ch, join: func() (Response, error) { return resp, err }}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{newTransport("reset"), true},
		{newProvider("bad"), false},
		{newHTTP(408, "timeout"), true},
		{newHTTP(409, "conflict"), true},
		{newHTTP(429, "rate"), true},
		{newHTTP(500, "server"), true},
		{newHTTP(502, "gateway"), true},
		{newHTTP(503, "unavail"), true},
		{newHTTP(599, "weird"), true},
		{newHTTP(400, "bad"), false},
		{newHTTP(401, "unauth"), false},
		{newHTTP(403, "forbidden"), false},
		{newHTTP(404, "missing"), false},
		{newHTTP(499, "client closed"), false},
		{errPlain("boom"), false},
	}
	for _, c := range cases {
		if got := retryable(c.err); got != c.want {
			t.Errorf("retryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

type plainErr string

func (e plainErr) Error() string { return string(e) }

func errPlain(s string) error { return plainErr(s) }

func TestRequestStreamSuccessForwardsEvents(t *testing.T) {
	call := ToolCall{ID: "c1", Name: "bash", Arguments: "{}"}
	resp := Response{Message: Message{Role: "assistant", Content: "hi"}, Usage: Usage{Input: 1, Output: 2}, StopReason: "stop"}
	h := handleWithResponse(resp, nil,
		StreamEvent{Kind: eventContent, Text: "hi"},
		StreamEvent{Kind: eventToolCall, Call: &call},
		StreamEvent{Kind: eventTokens, Usage: Usage{Input: 1, Output: 2}},
		StreamEvent{Kind: eventDone},
	)
	p := &fakeProvider{handles: []*StreamHandle{h}}
	sink := &recordingSink{}

	gotResp, gotCalls, err := requestStream(p, &runOptions{model: "m"}, []Message{}, &atomic.Bool{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if gotResp.Message.Content != "hi" {
		t.Errorf("content = %q", gotResp.Message.Content)
	}
	if len(gotCalls) != 1 || gotCalls[0].Name != "bash" || gotCalls[0].ID != "c1" {
		t.Errorf("calls = %+v", gotCalls)
	}
	if !reflect.DeepEqual(sink.deltas, []string{"hi"}) {
		t.Errorf("deltas = %v", sink.deltas)
	}
	if len(sink.tok) != 1 || sink.tok[0] != (Usage{Input: 1, Output: 2}) {
		t.Errorf("tokens = %v", sink.tok)
	}
	if p.idx != 1 {
		t.Errorf("provider idx = %d", p.idx)
	}
}

func TestRequestStreamNoRetryAfterForwardedEvents(t *testing.T) {
	h := handleWithResponse(Response{}, newTransport("conn reset"), StreamEvent{Kind: eventContent, Text: "partial"})
	p := &fakeProvider{handles: []*StreamHandle{h}}
	_, _, err := requestStream(p, &runOptions{}, []Message{}, &atomic.Bool{}, &recordingSink{})
	if err == nil {
		t.Fatal("expected error")
	}
	if p.idx != 1 {
		t.Errorf("provider should not retry after forwarded events, idx = %d", p.idx)
	}
}

func TestRequestStreamRetriesTransportOnce(t *testing.T) {
	h1 := handleWithResponse(Response{}, newTransport("conn reset"))
	h2 := handleWithResponse(Response{Message: Message{Role: "assistant", Content: "ok"}}, nil, StreamEvent{Kind: eventDone})
	p := &fakeProvider{handles: []*StreamHandle{h1, h2}}
	got, _, err := requestStream(p, &runOptions{}, []Message{}, &atomic.Bool{}, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Content != "ok" {
		t.Errorf("content = %q", got.Message.Content)
	}
	if p.idx != 2 {
		t.Errorf("expected one retry, idx = %d", p.idx)
	}
}

func TestRequestStreamRetriesHTTP500Once(t *testing.T) {
	h1 := handleWithResponse(Response{}, newHTTP(500, "boom"))
	h2 := handleWithResponse(Response{Message: Message{Role: "assistant", Content: "ok"}}, nil, StreamEvent{Kind: eventDone})
	p := &fakeProvider{handles: []*StreamHandle{h1, h2}}
	got, _, err := requestStream(p, &runOptions{}, []Message{}, &atomic.Bool{}, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Content != "ok" {
		t.Errorf("content = %q", got.Message.Content)
	}
	if p.idx != 2 {
		t.Errorf("expected retry after 500, idx = %d", p.idx)
	}
}

func TestRequestStreamDoesNotRetryProviderError(t *testing.T) {
	h := handleWithResponse(Response{}, newProvider("bad gateway"))
	p := &fakeProvider{handles: []*StreamHandle{h}}
	_, _, err := requestStream(p, &runOptions{}, []Message{}, &atomic.Bool{}, &recordingSink{})
	if err == nil {
		t.Fatal("expected error")
	}
	if p.idx != 1 {
		t.Errorf("provider error should not retry, idx = %d", p.idx)
	}
}

func TestRequestStreamDoesNotRetryHTTP400(t *testing.T) {
	h := handleWithResponse(Response{}, newHTTP(400, "bad request"))
	p := &fakeProvider{handles: []*StreamHandle{h}}
	_, _, err := requestStream(p, &runOptions{}, []Message{}, &atomic.Bool{}, &recordingSink{})
	if err == nil {
		t.Fatal("expected error")
	}
	if p.idx != 1 {
		t.Errorf("400 should not retry, idx = %d", p.idx)
	}
}

func TestRequestStreamCancelInterruptsBackoff(t *testing.T) {
	cancel := &atomic.Bool{}
	cancel.Store(true)
	h := handleWithResponse(Response{}, newTransport("conn reset"))
	p := &fakeProvider{handles: []*StreamHandle{h}}
	_, _, err := requestStream(p, &runOptions{}, []Message{}, cancel, &recordingSink{})
	if err == nil {
		t.Fatal("expected error")
	}
	if p.idx != 1 {
		t.Errorf("cancel should stop retries, idx = %d", p.idx)
	}
}

func TestRunStreamToolExecutionThenDone(t *testing.T) {
	tool := Tool{
		Name: "bash", Description: "d", Parameters: json.RawMessage(`{}`),
		Run: func(arguments string, progress func(string)) string { return "tool-output " + arguments },
	}
	c1 := ToolCall{ID: "call-1", Name: "bash", Arguments: "echo hi"}
	turn0 := Response{Message: Message{Role: "assistant", ToolCalls: []ToolCall{c1}}, Usage: Usage{Input: 5, Output: 1}, StopReason: "tool_calls"}
	turn1 := Response{Message: Message{Role: "assistant", Content: "final"}, Usage: Usage{Input: 7, Output: 3}, StopReason: "stop"}
	p := &fakeProvider{handles: []*StreamHandle{
		handleWithResponse(turn0, nil, StreamEvent{Kind: eventToolCall, Call: &c1}, StreamEvent{Kind: eventDone}),
		handleWithResponse(turn1, nil, StreamEvent{Kind: eventContent, Text: "final"}, StreamEvent{Kind: eventDone}),
	}}
	sink := &recordingSink{}
	end := runStream(p, &runOptions{model: "m", tools: []Tool{tool}, maxTurns: 4}, []Message{{Role: "user", Content: "hello"}}, &atomic.Bool{}, sink)

	if end.outcome != outcomeDone {
		t.Errorf("outcome = %v", end.outcome)
	}
	if end.usage.Input != 12 || end.usage.Output != 4 {
		t.Errorf("usage = %+v", end.usage)
	}
	if len(end.messages) != 4 {
		t.Fatalf("messages = %+v", end.messages)
	}
	if end.messages[0].Role != "user" || end.messages[1].Role != "assistant" || end.messages[2].Role != "tool" || end.messages[3].Role != "assistant" {
		t.Errorf("roles = %v %v %v %v", end.messages[0].Role, end.messages[1].Role, end.messages[2].Role, end.messages[3].Role)
	}
	if end.messages[2].Content != "tool-output echo hi" || end.messages[3].Content != "final" {
		t.Errorf("contents = %q %q", end.messages[2].Content, end.messages[3].Content)
	}
	if len(sink.assist) != 2 || len(sink.tools) != 1 || len(sink.starts) != 1 || sink.dones != 2 {
		t.Errorf("sink counters assist=%d tools=%d starts=%d dones=%d", len(sink.assist), len(sink.tools), len(sink.starts), sink.dones)
	}
	if !reflect.DeepEqual(sink.deltas, []string{"final"}) {
		t.Errorf("deltas = %v", sink.deltas)
	}
}

func TestRunStreamMaxTurnsExhausted(t *testing.T) {
	c1 := ToolCall{ID: "call-1", Name: "bash", Arguments: "x"}
	turn0 := Response{Message: Message{Role: "assistant", ToolCalls: []ToolCall{c1}}, StopReason: "tool_calls"}
	p := &fakeProvider{handles: []*StreamHandle{
		handleWithResponse(turn0, nil, StreamEvent{Kind: eventToolCall, Call: &c1}, StreamEvent{Kind: eventDone}),
	}}
	end := runStream(p, &runOptions{model: "m", maxTurns: 1}, []Message{{Role: "user", Content: "hi"}}, &atomic.Bool{}, &recordingSink{})
	if end.outcome != outcomeMaxTurns {
		t.Errorf("outcome = %v", end.outcome)
	}
}

func TestRunStreamCancelImmediately(t *testing.T) {
	cancel := &atomic.Bool{}
	cancel.Store(true)
	p := &fakeProvider{}
	end := runStream(p, &runOptions{model: "m", maxTurns: 2}, []Message{{Role: "user", Content: "hi"}}, cancel, &recordingSink{})
	if end.outcome != outcomeCancelled {
		t.Errorf("outcome = %v", end.outcome)
	}
	if p.idx != 0 {
		t.Errorf("provider should not be called, idx = %d", p.idx)
	}
}
