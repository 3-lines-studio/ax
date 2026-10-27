package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = f
	defer func() { os.Stdout = old; _ = f.Close() }()
	fn()
	_ = f.Sync()
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeLines(t *testing.T, out string) []map[string]interface{} {
	t.Helper()
	var decoded []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad JSON line %q: %v", line, err)
		}
		decoded = append(decoded, m)
	}
	return decoded
}

func TestEventSinkEmitsAssistantDelta(t *testing.T) {
	sink := &eventSink{}
	out := captureStdout(t, func() { sink.assistantDelta("hi") })
	lines := decodeLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0]["type"] != "assistant_delta" || lines[0]["text"] != "hi" {
		t.Errorf("line = %v", lines[0])
	}
}

func TestEventSinkEmitsToolEvents(t *testing.T) {
	sink := &eventSink{}
	call := ToolCall{ID: "c1", Name: "bash", Arguments: "{}"}
	out := captureStdout(t, func() {
		sink.toolStart(call)
		sink.toolDelta(call, "partial")
		sink.toolResult(call)
		sink.assistantDone()
	})
	lines := decodeLines(t, out)
	if len(lines) != 4 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0]["type"] != "tool_start" || lines[0]["id"] != "c1" || lines[0]["name"] != "bash" {
		t.Errorf("tool_start = %v", lines[0])
	}
	if lines[1]["type"] != "tool_delta" || lines[1]["text"] != "partial" {
		t.Errorf("tool_delta = %v", lines[1])
	}
	if lines[2]["type"] != "tool_done" || lines[2]["id"] != "c1" {
		t.Errorf("tool_done = %v", lines[2])
	}
	if lines[3]["type"] != "assistant_done" {
		t.Errorf("assistant_done = %v", lines[3])
	}
}

func TestEventSinkEmitsAssistantAndTool(t *testing.T) {
	sink := &eventSink{}
	assistant := Message{Role: "assistant", Content: "answer"}
	tool := Message{Role: "tool", Content: "out", ToolCallID: "c1"}
	out := captureStdout(t, func() {
		sink.assistant(0, assistant, Usage{Input: 1, Output: 2})
		sink.tool(0, tool)
	})
	lines := decodeLines(t, out)
	if len(lines) != 3 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0]["type"] != "message" {
		t.Errorf("assistant message = %v", lines[0])
	}
	if lines[1]["type"] != "tool_result" || lines[1]["id"] != "c1" || lines[1]["output"] != "out" {
		t.Errorf("tool_result = %v", lines[1])
	}
	if lines[2]["type"] != "message" {
		t.Errorf("tool message = %v", lines[2])
	}
}

func TestEventSinkTokensSetsLastUsage(t *testing.T) {
	sink := &eventSink{model: "m"}
	out := captureStdout(t, func() { sink.tokens(3, 4, 5) })
	if sink.lastUsage == nil || *sink.lastUsage != (Usage{3, 4, 5}) {
		t.Errorf("lastUsage = %+v", sink.lastUsage)
	}
	lines := decodeLines(t, out)
	if lines[0]["type"] != "usage" || lines[0]["input"].(float64) != 3 || lines[0]["output"].(float64) != 4 || lines[0]["cached_input"].(float64) != 5 {
		t.Errorf("usage = %v", lines[0])
	}
}

func TestEventSinkSessionFlushOnRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	sink := &eventSink{session: path, model: "m"}
	sink.record(Message{Role: "user", Content: "hello"})
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Message == nil || entries[0].Message.Content != "hello" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestEventSinkHoldsToolBatchUntilComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	sink := &eventSink{session: path, model: "m"}
	sink.record(Message{Role: "user", Content: "hello"})
	sink.record(Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "bash"}}})
	entriesAfterAssistant, _ := loadPath(path)
	if len(entriesAfterAssistant) != 1 {
		t.Errorf("assistant should not flush until results, got %d", len(entriesAfterAssistant))
	}
	sink.record(Message{Role: "tool", Content: "out", ToolCallID: "c1"})
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs := contextMessages(entries)
	if len(msgs) != 3 {
		t.Fatalf("msgs = %+v", msgs)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "c1" {
		t.Errorf("last = %+v", msgs[2])
	}
}

func TestEventSinkFinalizeWritesRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	sink := &eventSink{session: path, model: "m"}
	sink.record(Message{Role: "user", Content: "hello"})
	full := []Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "unexpected tail"}}
	sink.finalize(full, 0)
	msgs := contextMessages(mustLoad(t, path))
	if len(msgs) != 2 || msgs[1].Content != "unexpected tail" {
		t.Errorf("msgs = %+v", msgs)
	}
}

func mustLoad(t *testing.T, path string) []sessionEntry {
	t.Helper()
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestOneShotEventsFraming(t *testing.T) {
	body := "" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("AX_TOOLS", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", "")

	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	oldStdin := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = oldStdin }()

	cfg := cliConfig{base: srv.URL, model: "test-model"}
	bot := resolveBotCtx(t.TempDir())

	out := captureStdout(t, func() {
		oneShotEvents(cfg, fileConfig{}, bot, []string{"hello"})
	})
	lines := decodeLines(t, out)
	if len(lines) < 4 {
		t.Fatalf("lines = %v", lines)
	}
	var types []string
	lastEvent := map[string]interface{}{}
	for _, m := range lines {
		tv, _ := m["type"].(string)
		if tv == "" {
			continue
		}
		types = append(types, tv)
		lastEvent = m
	}
	if types[0] != "protocol" {
		t.Errorf("first type = %q", types[0])
	}
	if !containsString(types, "assistant_delta") || !containsString(types, "message") || !containsString(types, "assistant_done") {
		t.Errorf("missing framing events: %v", types)
	}
	if !containsString(types, "result") {
		t.Errorf("no result event: %v", types)
	}
	if lastEvent["type"] != "done" || lastEvent["outcome"] != "done" {
		t.Errorf("last event = %v", lastEvent)
	}
	for _, m := range lines {
		if m["type"] == "error" {
			t.Errorf("unexpected error event: %v", m)
		}
	}
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func TestEventFailureFramingSubprocess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "AX_TEST_HELPER=1", "AX_TEST_HELPER_MODE=eventfail")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	lines := decodeLines(t, out.String())
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0]["type"] != "error" || lines[0]["message"] != "boom" {
		t.Errorf("error line = %v", lines[0])
	}
	if lines[1]["type"] != "done" || lines[1]["outcome"] != "failed" {
		t.Errorf("done line = %v", lines[1])
	}
	if !reflect.DeepEqual([]string{"error", "done"}, eventTypes(lines)) {
		t.Errorf("framing = %v", eventTypes(lines))
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("AX_TEST_HELPER") != "1" {
		return
	}
	switch os.Getenv("AX_TEST_HELPER_MODE") {
	case "eventfail":
		eventFailure("boom")
	default:
		os.Exit(0)
	}
}

func eventTypes(lines []map[string]interface{}) []string {
	var out []string
	for _, m := range lines {
		tv, _ := m["type"].(string)
		out = append(out, tv)
	}
	return out
}
