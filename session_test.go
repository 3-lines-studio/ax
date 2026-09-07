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

func TestParseEntryLineEmptyObjectOddity(t *testing.T) {
	entry, ok := parseEntryLine(`{}`)
	if !ok {
		t.Fatal("{} should parse as empty bare message")
	}
	if entry.Type != "message" || entry.Message == nil {
		t.Errorf("empty object entry = %+v", entry)
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
	os.WriteFile(path, []byte(content), 0o644)
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
	os.WriteFile(path, []byte(content), 0o644)
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
	os.WriteFile(path, []byte("this is not json\n"), 0o644)
	_, err := loadPath(path)
	if err == nil || !strings.Contains(err.Error(), "invalid session") {
		t.Errorf("err = %v", err)
	}
}

func TestAtomicWriteCreatesDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "out.txt")
	if err := atomicWrite(path, []byte("data")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "data" {
		t.Errorf("read back = %q err=%v", string(data), err)
	}
}

func TestAtomicWriteOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	os.WriteFile(path, []byte("old"), 0o644)
	if err := atomicWrite(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("read back = %q", string(data))
	}
}

func TestAtomicWriteRemovesTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := atomicWrite(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ax-tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestAppendMessagesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	m1 := Message{Role: "user", Content: "first"}
	m2 := Message{Role: "assistant", Content: "second"}
	if err := appendMessages(path, []Message{m1}); err != nil {
		t.Fatal(err)
	}
	if err := appendMessages(path, []Message{m2}); err != nil {
		t.Fatal(err)
	}
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs := contextMessages(entries)
	if len(msgs) != 2 || !reflect.DeepEqual(msgs[0], m1) || !reflect.DeepEqual(msgs[1], m2) {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestAppendMessagesCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "s.jsonl")
	if err := appendMessages(path, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Message == nil || entries[0].Message.Content != "hi" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestAppendUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := appendUsage(path, 10, 20, 5, nil, "gpt-4"); err != nil {
		t.Fatal(err)
	}
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Type != "usage" {
		t.Fatalf("entries = %+v", entries)
	}
	u := entries[0].Usage
	if u.Input != 10 || u.Output != 20 || u.CachedInput != 5 || u.Model != "gpt-4" {
		t.Errorf("usage = %+v", u)
	}
}

func TestAppendMessagesOverwritePreservesUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := appendUsage(path, 1, 2, 0, nil, "m"); err != nil {
		t.Fatal(err)
	}
	msg := Message{Role: "user", Content: "hi"}
	if err := appendMessages(path, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	entries, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Type != "usage" || entries[1].Message == nil {
		t.Errorf("order lost: %+v", entries)
	}
}
