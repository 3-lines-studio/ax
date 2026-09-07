package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type sessionEntry struct {
	Type    string     `json:"type"`
	Message *Message   `json:"message,omitempty"`
	Usage   *usageInfo `json:"usage,omitempty"`
}

type usageInfo struct {
	Input         int    `json:"input"`
	Output        int    `json:"output"`
	CachedInput   int    `json:"cached_input"`
	ContextInput  int    `json:"context_input"`
	ContextOutput int    `json:"context_output"`
	Window        *int   `json:"window,omitempty"`
	Model         string `json:"model"`
}

func contextMessages(entries []sessionEntry) []Message {
	var out []Message
	for _, e := range entries {
		if e.Type == "message" && e.Message != nil {
			out = append(out, *e.Message)
		}
	}
	return out
}

func parseEntryLine(line string) (sessionEntry, bool) {
	var entry sessionEntry
	if err := json.Unmarshal([]byte(line), &entry); err == nil && entry.Type != "" {
		return entry, true
	}
	var message Message
	if err := json.Unmarshal([]byte(line), &message); err == nil {
		return sessionEntry{Type: "message", Message: &message}, true
	}
	return sessionEntry{}, false
}

func loadPath(path string) ([]sessionEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session %s: %w", path, err)
	}
	var entries []sessionEntry
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		entry, ok := parseEntryLine(string(trimmed))
		if !ok {
			return nil, fmt.Errorf("invalid session %s", path)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func appendUsage(path string, input, output, cached int, window *int, model string) error {
	entry := sessionEntry{
		Type: "usage",
		Usage: &usageInfo{
			Input:         input,
			Output:        output,
			CachedInput:   cached,
			ContextInput:  input,
			ContextOutput: output,
			Window:        window,
			Model:         model,
		},
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode session %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create session dir %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open session %s: %w", path, err)
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write session %s: %w", path, err)
	}
	return nil
}

func appendMessages(path string, messages []Message) error {
	entries, err := loadPath(path)
	if err != nil {
		return err
	}
	for _, m := range messages {
		msg := m
		entries = append(entries, sessionEntry{Type: "message", Message: &msg})
	}
	var out []byte
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode session %s: %w", path, err)
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	if err := atomicWrite(path, out); err != nil {
		return fmt.Errorf("write session %s: %w", path, err)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	var tag uint64
	tag = atomic.AddUint64(&writeTag, 1)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".ax-tmp-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), tag))
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := file.Write(data)
	cerr := file.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

var writeTag uint64
