package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCommandArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"a b c", []string{"a", "b", "c"}},
		{`a "b c" d`, []string{"a", "b c", "d"}},
		{`a 'b c' d`, []string{"a", "b c", "d"}},
		{`"quoted"`, []string{"quoted"}},
		{`a  b`, []string{"a", "b"}},
	}
	for _, c := range cases {
		if got := parseCommandArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseCommandArgs(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSubstituteArgs(t *testing.T) {
	args := []string{"one", "two", "three"}
	cases := []struct {
		in   string
		want string
	}{
		{"$ARGUMENTS", "one two three"},
		{"$@", "one two three"},
		{"$1", "one"},
		{"$2", "two"},
		{"$9", ""},
		{"${2:-default}", "two"},
		{"${9:-default}", "default"},
		{"${@:1}", "one two three"},
		{"${@:2}", "two three"},
		{"${@:2:1}", "two"},
		{"no placeholders", "no placeholders"},
		{"pre $1 post", "pre one post"},
	}
	for _, c := range cases {
		if got := substituteArgs(c.in, args); got != c.want {
			t.Errorf("substituteArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFrontmatter(t *testing.T) {
	desc, body := parseFrontmatter("---\ndescription: stage and commit\n---\n\nstage everything now")
	if desc != "stage and commit" {
		t.Errorf("description = %q", desc)
	}
	if body != "stage everything now" {
		t.Errorf("body = %q", body)
	}
	desc2, body2 := parseFrontmatter("no frontmatter\nmore")
	if desc2 != "no frontmatter" {
		t.Errorf("description fallback = %q", desc2)
	}
	if body2 != "no frontmatter\nmore" {
		t.Errorf("body fallback = %q", body2)
	}
}

func TestContextMessagesSkipsUsage(t *testing.T) {
	msg := Message{Role: "user", Content: "hi"}
	entries := []sessionEntry{
		{Type: "message", Message: &msg},
		{Type: "usage", Usage: &usageInfo{Input: 5}},
	}
	got := contextMessages(entries)
	if len(got) != 1 || !reflect.DeepEqual(got[0], msg) {
		t.Errorf("contextMessages = %v", got)
	}
}

func TestParseEntryLineBareMessage(t *testing.T) {
	line := `{"Role":"user","Content":"hello"}`
	entry, ok := parseEntryLine(line)
	if !ok || entry.Type != "message" || entry.Message.Content != "hello" {
		t.Errorf("parseEntryLine bare message = %+v ok=%v", entry, ok)
	}
	entry2, ok2 := parseEntryLine(`{"type":"message","message":{"Role":"assistant","Content":"x"}}`)
	if !ok2 || entry2.Message.Role != "assistant" {
		t.Errorf("parseEntryLine tagged = %+v ok=%v", entry2, ok2)
	}
}

func TestSanitizeAndTail(t *testing.T) {
	if got := sanitize("a\x00b\tc\nd"); got != "ab\tc\nd" {
		t.Errorf("sanitize = %q", got)
	}
	if got := tail(strings.Repeat("x", maxOutput+50)); len(got) != maxOutput {
		t.Errorf("tail length = %d", len(got))
	}
}

func TestValidToolName(t *testing.T) {
	for _, good := range []string{"bash", "read_file", "data-query", "A1"} {
		if !validToolName(good) {
			t.Errorf("validToolName(%q) = false", good)
		}
	}
	for _, bad := range []string{"", "has space", "slash/name", "dot.name", "tool;inject"} {
		if validToolName(bad) {
			t.Errorf("validToolName(%q) = true", bad)
		}
	}
}

func TestSSEAccumulatesToolCall(t *testing.T) {
	acc := &streamAcc{}
	events := make(chan StreamEvent, 16)
	full := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
	if err := acc.feed([]byte(full), events); err != nil {
		t.Fatal(err)
	}
	acc.finish(events)
	close(events)

	var calls []ToolCall
	done := false
	for ev := range events {
		if ev.Kind == eventToolCall && ev.Call != nil {
			calls = append(calls, *ev.Call)
		}
		if ev.Kind == eventDone {
			done = true
		}
	}
	if !done {
		t.Error("no done event")
	}
	if len(calls) != 1 || calls[0].Name != "bash" || calls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if !strings.Contains(calls[0].Arguments, "echo hi") {
		t.Errorf("arguments = %q", calls[0].Arguments)
	}
}

func TestSSECRLFSplitAcrossChunkBoundary(t *testing.T) {
	acc := &streamAcc{}
	events := make(chan StreamEvent, 16)
	full := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\r\n\r\n"
	split := strings.Index(full, "\r")
	if err := acc.feed([]byte(full[:split]), events); err != nil {
		t.Fatal(err)
	}
	if err := acc.feed([]byte(full[split:]), events); err != nil {
		t.Fatal(err)
	}
	close(events)

	found := false
	for ev := range events {
		if ev.Kind == eventContent && ev.Text == "hi" {
			found = true
		}
	}
	if !found {
		t.Error("content 'hi' not parsed from split CRLF event")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	if backoff(1) > 8000 || backoff(1) < 0 {
		t.Errorf("backoff(1) out of range: %d", backoff(1))
	}
	for a := 1; a <= 10; a++ {
		if backoff(a) > 8000 {
			t.Errorf("backoff(%d) = %d exceeds cap", a, backoff(a))
		}
	}
}
