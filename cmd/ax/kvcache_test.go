package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRuntimeClockStaysOutOfTheStablePrefix(t *testing.T) {
	bot := resolveBotCtx(t.TempDir())
	fc := fileConfig{}
	stable, volatile := resolveSystemParts(cliConfig{model: "m", base: "https://api.deepseek.com", toolMode: "catalog"}, nil, bot, nil, nil, defaultCompactAt, defaultContextWindow)
	if strings.Contains(stable, "Time: ") {
		t.Fatalf("the stable prompt carries the clock:\n%s", stable)
	}
	if !strings.Contains(volatile, "Time: ") {
		t.Fatalf("volatile = %q", volatile)
	}
	_ = fc
}

func TestChainedRequestsAppendOnly(t *testing.T) {
	opts := &runOptions{model: "m", stable: "S", summary: "SUM", volatile: "V", strategy: kvcacheChained}
	cache := newKvcache(opts)
	history := []Message{{Role: "user", Content: "hi"}}
	cache.observe(opts, history)
	if cache.chained(opts) {
		t.Fatal("without tools the history may be rewritten in place")
	}
	first := cache.request(opts, history, "")
	if !strings.Contains(first.System, "SUM") || !strings.HasSuffix(first.System, "V") {
		t.Fatalf("the summary must live in the system before the clock: %q", first.System)
	}
	third := cache.request(opts, history, retryNote)
	if !strings.Contains(third.System, "## Run") {
		t.Fatalf("a retry must mark its note in the system: %q", third.System)
	}
}

func TestChainedToolsAppendAfterTheReasoningPrefix(t *testing.T) {
	tool := Tool{Name: "bash", Description: "Run a command", Parameters: []byte(`{"type":"object"}`)}
	opts := &runOptions{model: "m", stable: "S", summary: "SUM", volatile: "V", tools: []Tool{tool}, strategy: kvcacheChained}
	cache := newKvcache(opts)
	history := []Message{{Role: "user", Content: "hi"}}
	cache.observe(opts, history)
	if !cache.needsPrime(opts) {
		t.Fatal("the first request must persist the prefix")
	}
	first := cache.request(opts, history, "")
	if first.System != "SV" {
		t.Fatalf("system = %q, the reasoning-bearing prefix cannot carry the summary", first.System)
	}
	prime := cache.primeRequest(opts, history)
	if len(prime.Messages) != 1 || !strings.Contains(prime.Messages[0].Content, "S") || strings.Contains(prime.Messages[0].Content, "SUM") {
		t.Fatalf("prime = %+v", prime.Messages)
	}
	if len(first.Messages) != 1 {
		t.Fatalf("the first request must not chain onto anything: %+v", first.Messages)
	}
	history = append(history, Message{Role: "assistant", Content: "hello", ReasoningContent: "because"})
	tail := cache.request(opts, history, retryNote)
	if tail.System != first.System {
		t.Fatalf("system changed mid-run: %q -> %q", first.System, tail.System)
	}
	if len(tail.Messages) != 3 || tail.Messages[1].ReasoningContent != "because" || tail.Messages[2].Content != retryNote {
		t.Fatalf("the retry must be a new message after the confirmed turn: %+v", tail.Messages)
	}
}

func TestCompactionKeepsAReasoningHistoryPrefixIntact(t *testing.T) {
	tool := Tool{Name: "bash", Description: "Run a command", Parameters: []byte(`{"type":"object"}`)}
	opts := &runOptions{model: "m", stable: "S", summary: "FIRST", volatile: "V", tools: []Tool{tool}, strategy: kvcacheChained}
	cache := newKvcache(opts)
	history := []Message{{Role: "user", Content: "task"}}
	cache.observe(opts, history)
	opts.summary = "SECOND"
	cache.compacted()
	if !cache.needsPrime(opts) {
		t.Fatal("a compaction must persist the new prefix before the next request")
	}
	prime := cache.primeRequest(opts, history)
	req := cache.request(opts, history, "")
	if strings.Contains(req.System, "SECOND") || strings.Contains(prime.Messages[0].Content, "SECOND") {
		t.Fatalf("the new summary rewrote the reasoning prefix: %+v", req)
	}
}

func TestFoldedToolsKeepTheirDeclarationsInTheSystemOnly(t *testing.T) {
	tool := Tool{Name: "bash", Description: "Run a command", Parameters: []byte(`{"type":"object","properties":{"command":{"type":"string"}}}`)}
	enabled := true
	provider := OpenAI{BaseURL: "https://api.deepseek.com", APIKey: "k", FoldTools: &enabled}
	built, err := provider.buildRequest(&Request{Model: "m", System: "S", Tools: []Tool{tool}, Messages: []Message{{Role: "user", Content: "hi"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Tools    []any `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(built.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 0 {
		t.Fatalf("folded requests must declare no tools: %d", len(body.Tools))
	}
	if len(body.Messages) != 2 || !strings.Contains(body.Messages[0].Content, "### bash") || !strings.Contains(body.Messages[0].Content, "execute") {
		t.Fatalf("messages = %+v", body.Messages)
	}
	for _, msg := range body.Messages {
		if msg.Role == "system" {
			continue
		}
		if strings.Contains(msg.Content, "### bash") {
			t.Fatalf("the tool schema leaked into a %s message", msg.Role)
		}
	}
}

func TestRetryAfterAFailureKeepsThePrefix(t *testing.T) {
	provider := &scriptedProvider{reply: agentReply("answer")}
	opts := &runOptions{model: "m", stable: "S", volatile: "V", maxTurns: 1}
	cache := newKvcache(opts)
	history := []Message{{Role: "user", Content: "hello"}}
	cache.observe(opts, history)
	resp, _, err := requestStream(provider, opts, history, &atomic.Bool{}, &recordingSink{}, cache)
	if err != nil || resp.Message.Content != "answer" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if provider.requests[0].System != "SV" {
		t.Fatalf("system = %q", provider.requests[0].System)
	}
}

func TestPromptKeepsTheToolSchemaAfterTheSystemText(t *testing.T) {
	opts := &runOptions{model: "m", stable: "STABLE", volatile: "VOLATILE", strategy: kvcacheAppend}
	cache := newKvcache(opts)
	req := cache.request(opts, []Message{{Role: "user", Content: "hi"}}, "")
	if req.System != "STABLEVOLATILE" {
		t.Fatalf("system = %q", req.System)
	}
}

func TestCompactPlannerPromptFollowsPiG(t *testing.T) {
	prompt := compactPrompt("[User]: hi", "", "")
	if !strings.HasPrefix(prompt, "<conversation>\n") {
		t.Fatalf("prompt = %q", prompt)
	}
	for _, want := range []string{"## Goal", "## Progress", "### Done", "### In Progress", "### Blocked", "## Key Decisions", "## Next Steps", "## Critical Context"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q", want)
		}
	}
	update := compactPrompt("[User]: hi", "OLD", "")
	if !strings.Contains(update, "<previous-summary>\nOLD\n</previous-summary>") || strings.Contains(update, "Create a structured context checkpoint") {
		t.Fatalf("update prompt = %q", update)
	}
}

func TestCompactChunksALongHistory(t *testing.T) {
	provider := &scriptedProvider{reply: agentReply("S")}
	s := &Summarizer{provider: provider, model: "m", cancel: &atomic.Bool{}}
	history := []Message{{Role: "user", Content: strings.Repeat("x", 200_000)}, {Role: "user", Content: strings.Repeat("y", 200_000)}}
	window := len(renderTranscript(history)) / compactEstimateDivisor
	summary, err := s.summarize(context.Background(), history, "", "", window)
	if err != nil || summary != "S" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	if provider.count() < 2 {
		t.Fatalf("requests = %d, a history over the window must be summarized in chunks", provider.count())
	}
	for i, req := range provider.requests[1:] {
		if !strings.Contains(req.Messages[0].Content, "<previous-summary>") {
			t.Fatalf("chunk %d does not carry the previous summary", i+1)
		}
	}
}

func TestCompactFailureKeepsTheHistoryAndAddsANote(t *testing.T) {
	provider := &scriptedProvider{reply: agentReply("")}
	engine := &compactor{provider: provider, model: "m", base: "base", summary: "OLD"}
	history := []Message{{Role: "user", Content: "task"}, {Role: "user", Content: "more"}}
	next, system, err := engine.compact(context.Background(), history, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != len(history) {
		t.Fatalf("history = %+v", next)
	}
	if !strings.Contains(system, "OLD") {
		t.Fatalf("system = %q", system)
	}
}

func TestCompactSummaryCarriesTheTouchedFiles(t *testing.T) {
	provider := &scriptedProvider{reply: agentReply("SUM")}
	engine := &compactor{provider: provider, model: "m", base: "base"}
	history := []Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Name: "fs.read", Arguments: `{"path":"/a"}`}, {ID: "2", Name: "fs.edit", Arguments: `{"path":"/b"}`}}},
		{Role: "tool", Content: "ok", ToolCallID: "1"},
		{Role: "user", Content: "more"},
	}
	_, system, err := engine.compact(context.Background(), history, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(system, "<read-files>\n/a\n</read-files>") || !strings.Contains(system, "<modified-files>\n/b\n</modified-files>") {
		t.Fatalf("system = %q", system)
	}
}
