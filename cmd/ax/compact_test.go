package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type scriptedProvider struct {
	mu       sync.Mutex
	requests []*Request
	reply    func(req *Request) Response
}

func (p *scriptedProvider) stream(req *Request, cancel *atomic.Bool) *StreamHandle {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	resp := p.reply(req)
	event := StreamEvent{Kind: eventContent, Text: resp.Message.Content}
	return handleWithResponse(resp, nil, event)
}

func (p *scriptedProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func agentReply(text string) func(*Request) Response {
	return func(req *Request) Response {
		return Response{Message: Message{Role: "assistant", Content: text}}
	}
}

func TestDeriveSessionAppliesCompaction(t *testing.T) {
	user1 := Message{Role: "user", Content: "first"}
	assistant1 := Message{Role: "assistant", Content: "one"}
	user2 := Message{Role: "user", Content: "second"}
	entries := []sessionEntry{
		{Type: entryRunStart, RunID: "a"},
		{Type: entryMessage, Message: &user1},
		{Type: entryMessage, Message: &assistant1},
		{Type: entryRunEnd, RunID: "a", Outcome: "done"},
		{Type: entryCompaction, Upto: 2, Summary: "the summary"},
		{Type: entryRunStart, RunID: "b"},
		{Type: entryMessage, Message: &user2},
	}
	state := deriveSession(entries)
	if state.Summary != "the summary" {
		t.Fatalf("summary = %q", state.Summary)
	}
	if len(state.Messages) != 1 || state.Messages[0].Content != "second" {
		t.Fatalf("messages = %+v", state.Messages)
	}
}

func TestDeriveSessionCompactionDropsOrphanedToolResults(t *testing.T) {
	user := Message{Role: "user", Content: "run it"}
	call := ToolCall{ID: "c1", Name: "bash", Arguments: "{}"}
	assistant := Message{Role: "assistant", ToolCalls: []ToolCall{call}}
	tool := Message{Role: "tool", Content: "out", ToolCallID: "c1"}
	next := Message{Role: "user", Content: "and now"}
	entries := []sessionEntry{
		{Type: entryMessage, Message: &user},
		{Type: entryMessage, Message: &assistant},
		{Type: entryToolResult, Call: &call, Output: "out"},
		{Type: entryMessage, Message: &tool},
		{Type: entryCompaction, Upto: 2, Summary: "s"},
		{Type: entryMessage, Message: &next},
	}
	state := deriveSession(entries)
	if len(state.Messages) != 1 || state.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", state.Messages)
	}
}

func TestEstimateTokensGrowsWithHistory(t *testing.T) {
	small := estimateTokens("system", []Message{{Role: "user", Content: "hi"}})
	large := estimateTokens("system", []Message{{Role: "user", Content: strings.Repeat("x", 3000)}})
	if small <= 0 || large <= small {
		t.Fatalf("small=%d large=%d", small, large)
	}
	if small > 100 {
		t.Errorf("small estimate = %d", small)
	}
	withImage := estimateTokens("", []Message{{Role: "user", ContentParts: []ContentPart{{Type: "image", Image: &ImageRef{URL: "https://x/y.png"}}}}})
	if withImage < 1024 {
		t.Errorf("image estimate = %d", withImage)
	}
}

func TestRunStreamCompactsBeforeTheFirstRequest(t *testing.T) {
	provider := &scriptedProvider{reply: agentReply("answer")}
	var calls []bool
	opts := &runOptions{
		model:         "m",
		system:        strings.Repeat("s", 900),
		maxTurns:      1,
		compactAt:     0.5,
		contextWindow: 100,
		compact: func(ctx context.Context, history []Message, midRun bool) ([]Message, string, error) {
			calls = append(calls, midRun)
			return []Message{{Role: "user", Content: "kept"}}, "condensed system", nil
		},
	}
	end := runStream(context.Background(), provider, opts, []Message{{Role: "user", Content: "hello"}}, &atomic.Bool{}, &recordingSink{})
	if end.outcome != outcomeDone {
		t.Fatalf("outcome = %v (%s)", end.outcome, end.failMsg)
	}
	if len(calls) != 1 || calls[0] {
		t.Fatalf("compaction calls = %v", calls)
	}
	req := provider.requests[0]
	if req.System != "condensed system" {
		t.Errorf("system = %q", req.System)
	}
	if len(req.Messages) != 1 || req.Messages[0].Content != "kept" {
		t.Errorf("messages = %+v", req.Messages)
	}
}

func TestRunStreamSkipsCompactionWhenDisabled(t *testing.T) {
	provider := &scriptedProvider{reply: agentReply("answer")}
	opts := &runOptions{
		model:     "m",
		system:    strings.Repeat("s", 9000),
		maxTurns:  1,
		compactAt: 0,
	}
	end := runStream(context.Background(), provider, opts, []Message{{Role: "user", Content: "hello"}}, &atomic.Bool{}, &recordingSink{})
	if end.outcome != outcomeDone || provider.count() != 1 {
		t.Fatalf("outcome=%v requests=%d", end.outcome, provider.count())
	}
}

func TestCompactorRecordsCutoffAndEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	log, err := openSessionLog(path)
	if err != nil {
		t.Fatal(err)
	}
	user1 := Message{Role: "user", Content: "first"}
	assistant1 := Message{Role: "assistant", Content: "one"}
	user2 := Message{Role: "user", Content: "second"}
	for _, msg := range []Message{user1, assistant1, user2} {
		copied := msg
		if err := log.append(sessionEntry{Type: entryMessage, Message: &copied}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &scriptedProvider{reply: agentReply("WORKING SUMMARY")}
	var events []map[string]any
	engine := &compactor{
		provider: provider,
		model:    "m",
		base:     "base system",
		log:      log,
		runID:    "run-1",
		emit: func(v any) {
			data, _ := json.Marshal(v)
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			events = append(events, m)
		},
	}
	history := []Message{user1, assistant1, user2}
	next, system, err := engine.compact(context.Background(), history, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Content != "second" {
		t.Fatalf("next = %+v", next)
	}
	if !strings.Contains(system, "base system") || !strings.Contains(system, "WORKING SUMMARY") {
		t.Errorf("system = %q", system)
	}
	if len(events) != 1 || events[0]["type"] != "compaction" {
		t.Fatalf("events = %v", events)
	}
	if err := log.close(); err != nil {
		t.Fatal(err)
	}
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Type != entryCompaction || last.Upto != 2 || last.Summary != "WORKING SUMMARY" || last.RunID != "run-1" {
		t.Fatalf("entry = %+v", last)
	}
	state := deriveSession(entries)
	if state.Summary != "WORKING SUMMARY" || len(state.Messages) != 1 || state.Messages[0].Content != "second" {
		t.Fatalf("state = %+v", state)
	}
	if !strings.Contains(provider.requests[0].Messages[0].Content, "CONVERSATION:") {
		t.Errorf("summarizer prompt = %q", provider.requests[0].Messages[0].Content)
	}
	if provider.requests[0].System != compactSystemPrompt {
		t.Errorf("summarizer system = %q", provider.requests[0].System)
	}
}

func TestCompactorMidRunKeepsAContinueTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	log, err := openSessionLog(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{reply: agentReply("SUMMARY")}
	engine := &compactor{provider: provider, model: "m", base: "base", log: log, runID: "r"}
	history := []Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "working"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "bash"}}},
		{Role: "tool", Content: "output", ToolCallID: "c"},
	}
	next, system, err := engine.compact(context.Background(), history, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Role != "user" || next[0].Content != continueNote {
		t.Fatalf("next = %+v", next)
	}
	if !strings.Contains(system, "SUMMARY") {
		t.Errorf("system = %q", system)
	}
	if err := log.close(); err != nil {
		t.Fatal(err)
	}
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Type != entryCompaction || entries[0].Upto != len(history) {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[1].Type != entryMessage || entries[1].Message == nil || entries[1].Message.Content != continueNote {
		t.Fatalf("continue entry = %+v", entries[1])
	}
	state := deriveSession(entries)
	if state.Summary != "SUMMARY" || len(state.Messages) != 1 || state.Messages[0].Content != continueNote {
		t.Fatalf("state = %+v", state)
	}
}

func TestCompactCommandAppendsSummary(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"COMPACTED\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "thread.jsonl")
	log, err := openSessionLog(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []Message{
		{Role: "user", Content: "first task"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "second task"},
	} {
		copied := msg
		if err := log.append(sessionEntry{Type: entryMessage, Message: &copied}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", "")
	bot := resolveBotCtx(dir)
	out := captureStdout(t, func() {
		runCompactCommand(cliConfig{base: srv.URL, model: "test-model"}, fileConfig{}, bot, []string{"--session", path})
	})
	if !strings.Contains(out, `"type":"compaction"`) || !strings.Contains(out, `"outcome":"compact"`) {
		t.Errorf("stdout = %q", out)
	}
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Type != entryCompaction || last.Upto != 2 || last.Summary != "COMPACTED" {
		t.Fatalf("entry = %+v", last)
	}
	if !strings.Contains(body, "CONVERSATION:") || !strings.Contains(body, "first task") {
		t.Errorf("summarizer request = %q", body)
	}
}
