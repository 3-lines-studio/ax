package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseEntryLineForms(t *testing.T) {
	entry, ok := parseEntryLine(`{"type":"usage","usage":{"input":3,"output":2,"cached_input":1,"model":"m"}}`)
	if !ok || entry.Type != "usage" || entry.Usage == nil || entry.Usage.Input != 3 || entry.Usage.Model != "m" {
		t.Errorf("usage entry = %+v ok=%v", entry, ok)
	}

	entry2, ok2 := parseEntryLine(`{"type":"message","message":{"Role":"user","Content":"x"}}`)
	if !ok2 || entry2.Message == nil || entry2.Message.Content != "x" {
		t.Errorf("tagged message = %+v ok=%v", entry2, ok2)
	}

	entry3, ok3 := parseEntryLine(`{"Role":"assistant","Content":"y"}`)
	if !ok3 || entry3.Type != "message" || entry3.Message == nil || entry3.Message.Role != "assistant" {
		t.Errorf("bare message = %+v ok=%v", entry3, ok3)
	}

	if _, ok4 := parseEntryLine("not json"); ok4 {
		t.Error("invalid line should not parse")
	}
}

func TestContextMessagesProjectsOnlyMessages(t *testing.T) {
	a := Message{Role: "user", Content: "a"}
	b := Message{Role: "assistant", Content: "b"}
	entries := []sessionEntry{
		{Type: "message", Message: &a},
		{Type: "usage", Usage: &usageInfo{Input: 1}},
		{Type: "message", Message: &b},
	}
	got := contextMessages(entries)
	if len(got) != 2 || !reflect.DeepEqual(got[0], a) || !reflect.DeepEqual(got[1], b) {
		t.Errorf("contextMessages = %+v", got)
	}
}

func TestLoadPathMissingFile(t *testing.T) {
	entries, err := loadPath(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %v", entries)
	}
}

func TestLoadPathReadsMixedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	content := `{"type":"message","message":{"Role":"user","Content":"hi"}}` + "\n" +
		`{"Role":"assistant","Content":"yo"}` + "\n" +
		`{"type":"usage","usage":{"input":1,"output":1,"model":"m"}}` + "\n"
	_ = os.WriteFile(path, []byte(content), 0o644)
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	msgs := contextMessages(entries)
	if len(msgs) != 2 || msgs[0].Content != "hi" || msgs[1].Content != "yo" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestLoadPathSkipsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	content := "{\"type\":\"message\",\"message\":{\"Role\":\"user\",\"Content\":\"a\"}}\n\n   \n{\"type\":\"message\",\"message\":{\"Role\":\"user\",\"Content\":\"b\"}}\n"
	_ = os.WriteFile(path, []byte(content), 0o644)
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2", len(entries))
	}
}

func TestLoadPathInvalidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	_ = os.WriteFile(path, []byte("this is not json\n"), 0o644)
	_, err := loadPath(path)
	if err == nil || !strings.Contains(err.Error(), "invalid session") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadPathToleratesTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	valid := `{"type":"message","message":{"Role":"user","Content":"ok"}}` + "\n"
	_ = os.WriteFile(path, []byte(valid+`{"type":"message","mess`), 0o644)
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Message == nil || entries[0].Message.Content != "ok" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestSessionLogAppendPreservesPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	log, err := openSessionLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.append(sessionEntry{Type: entryRunStart, RunID: "r1"}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.append(sessionEntry{Type: entryRunEnd, RunID: "r1", Outcome: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := log.close(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(second), string(first)) {
		t.Errorf("append rewrote the log:\nfirst=%q\nsecond=%q", first, second)
	}
	if len(second) <= len(first) {
		t.Error("append did not grow the log")
	}
}

func TestSessionLogExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	first, err := openSessionLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.close() }()
	if _, err := openSessionLog(path); err == nil {
		t.Fatal("second opener should fail while the session is locked")
	}
}

func TestDeriveRepairsInterruptedTool(t *testing.T) {
	user := Message{Role: "user", Content: "do it"}
	assistant := Message{Role: "assistant", Content: "", ToolCalls: []ToolCall{
		{ID: "a", Name: "bash", Arguments: "{}"},
		{ID: "b", Name: "read", Arguments: "{}"},
	}}
	entries := []sessionEntry{
		{Type: entryRunStart, RunID: "r1"},
		{Type: entryMessage, Message: &user},
		{Type: entryMessage, Message: &assistant},
		{Type: entryToolIntent, Call: &assistant.ToolCalls[0], Target: "fsx.write"},
		{Type: entryToolResult, Call: &assistant.ToolCalls[1], Output: "second"},
	}
	state := deriveSession(entries)
	if len(state.Messages) != 4 {
		t.Fatalf("messages = %+v", state.Messages)
	}
	if state.Messages[2].Role != "tool" || state.Messages[2].ToolCallID != "a" || !strings.Contains(state.Messages[2].Content, "interrupted") || !strings.Contains(state.Messages[2].Content, "fsx.write") {
		t.Errorf("repaired first result = %+v", state.Messages[2])
	}
	if state.Messages[3].ToolCallID != "b" || state.Messages[3].Content != "second" {
		t.Errorf("second result = %+v", state.Messages[3])
	}
	if state.Repaired != 1 || !state.OpenRun {
		t.Errorf("state = %+v", state)
	}
}

func TestDeriveDropsPartialAndOrphanResults(t *testing.T) {
	user := Message{Role: "user", Content: "hi"}
	partial := Message{Role: "assistant", Content: "half"}
	entries := []sessionEntry{
		{Type: entryRunStart, RunID: "r1"},
		{Type: entryMessage, Message: &user},
		{Type: entryAssistantPartial, Message: &partial},
		{Type: entryToolResult, Call: &ToolCall{ID: "orphan"}, Output: "ignored"},
	}
	state := deriveSession(entries)
	if len(state.Messages) != 1 || state.Messages[0].Content != "hi" {
		t.Errorf("messages = %+v", state.Messages)
	}
	if state.LastPartial == nil || state.LastPartial.Content != "half" {
		t.Errorf("partial = %+v", state.LastPartial)
	}
}

func TestDeriveClosesRun(t *testing.T) {
	entries := []sessionEntry{
		{Type: entryRunStart, RunID: "r1"},
		{Type: entryRunEnd, RunID: "r1", Outcome: "done"},
	}
	state := deriveSession(entries)
	if state.OpenRun {
		t.Error("run should be closed")
	}
}

func TestDeriveUsageTotals(t *testing.T) {
	entries := []sessionEntry{
		{Type: entryUsage, Usage: &usageInfo{Input: 10, Output: 2, CachedInput: 7, Reasoning: 1}},
		{Type: entryUsage, Usage: &usageInfo{Input: 5, Output: 3, CachedInput: 4, Reasoning: 2}},
	}
	state := deriveSession(entries)
	if state.Usage != (Usage{Input: 15, Output: 5, CachedInput: 11, Reasoning: 3}) {
		t.Errorf("usage = %+v", state.Usage)
	}
}

func TestDeriveLegacyToolMessage(t *testing.T) {
	user := Message{Role: "user", Content: "hi"}
	assistant := Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read"}}}
	tool := Message{Role: "tool", Content: "out", ToolCallID: "c1"}
	entries := []sessionEntry{
		{Type: entryMessage, Message: &user},
		{Type: entryMessage, Message: &assistant},
		{Type: entryMessage, Message: &tool},
	}
	state := deriveSession(entries)
	if len(state.Messages) != 3 || state.Messages[2].Content != "out" {
		t.Errorf("messages = %+v", state.Messages)
	}
}

func TestDeriveSessionNamesInterruptedCalls(t *testing.T) {
	entries := []sessionEntry{
		{Type: entryRunStart, RunID: "r1"},
		{Type: entryMessage, Message: &Message{Role: "user", Content: "fix it"}},
		{Type: entryMessage, Message: &Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "execute"}}}},
		{Type: entryToolIntent, Call: &ToolCall{ID: "c1"}, Target: "fsx.write"},
	}
	state := deriveSession(entries)
	if !state.OpenRun {
		t.Fatal("run should be open")
	}
	if len(state.Interrupted) != 1 || state.Interrupted[0] != "fsx.write" {
		t.Fatalf("interrupted = %v", state.Interrupted)
	}
	tool := state.Messages[len(state.Messages)-1]
	if tool.Role != "tool" || !strings.Contains(tool.Content, "interrupted") || !strings.Contains(tool.Content, "fsx.write") {
		t.Fatalf("last message = %+v", tool)
	}
	prompt := resumePrompt(state)
	if !strings.Contains(prompt, "Resume the interrupted run") || !strings.Contains(prompt, "fsx.write") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "check the state") || !strings.Contains(prompt, "Do not start the task over") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestResumePromptWithoutInterruptedCalls(t *testing.T) {
	prompt := resumePrompt(sessionState{OpenRun: true})
	if strings.Contains(prompt, "never recorded a result") {
		t.Fatalf("prompt invented a call: %q", prompt)
	}
	if !strings.Contains(prompt, "Resume the interrupted run") {
		t.Fatalf("prompt = %q", prompt)
	}
}
