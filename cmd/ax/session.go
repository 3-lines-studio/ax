package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session log entry types. The file is append-only JSONL: every line is one
// event, older lines are never rewritten, and a reader tolerates a torn final
// line left by a crash mid-append.
const (
	entryMessage          = "message"
	entryUsage            = "usage"
	entryRunStart         = "run_start"
	entryRunEnd           = "run_end"
	entryAssistantPartial = "assistant_partial"
	entryToolIntent       = "tool_intent"
	entryToolResult       = "tool_result"
	entryCompaction       = "compaction"
)

// interruptedToolOutput is what the model sees for a tool call whose outcome
// was never recorded. The side effects are unknown, so it is never re-run.
const interruptedToolOutput = "error: tool call interrupted: the result was never recorded and its side effects are unknown."

type sessionEntry struct {
	Type string `json:"type"`

	// message / assistant_partial
	Message *Message `json:"message,omitempty"`

	// usage
	Usage *usageInfo `json:"usage,omitempty"`

	// run lifecycle and tool records
	RunID       string    `json:"run_id,omitempty"`
	Turn        int       `json:"turn,omitempty"`
	At          int64     `json:"at,omitempty"` // unix milliseconds
	Call        *ToolCall `json:"call,omitempty"`
	Target      string    `json:"target,omitempty"`
	Output      string    `json:"output,omitempty"`
	IsError     bool      `json:"is_error,omitempty"`
	Interrupted bool      `json:"interrupted,omitempty"`
	Outcome     string    `json:"outcome,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`

	// compaction: how many projected messages the summary replaces, and the
	// summary itself. Older entries stay in the log; readers drop the prefix.
	Upto    int      `json:"upto,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Model   string   `json:"model,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Tools   []string `json:"tools,omitempty"`
}

type usageInfo struct {
	Input         int    `json:"input"`
	Output        int    `json:"output"`
	CachedInput   int    `json:"cached_input"`
	Reasoning     int    `json:"reasoning,omitempty"`
	ContextInput  int    `json:"context_input"`
	ContextOutput int    `json:"context_output"`
	Window        *int   `json:"window,omitempty"`
	Model         string `json:"model"`
}

// sessionState is the projection of the append-only log that the engine
// needs: the message history for the next request, whether the previous run
// ended, and how many interrupted tool calls had to be repaired.
type sessionState struct {
	Messages    []Message
	Summary     string
	OpenRun     bool
	LastRunID   string
	LastPartial *Message
	Repaired    int
	Usage       Usage
}

// contextMessages projects the durable log into a provider-valid message
// history. Incomplete assistant turns are dropped, and every tool call
// without a recorded result gets an interrupted result in call order.
func contextMessages(entries []sessionEntry) []Message {
	return deriveSession(entries).Messages
}

func deriveSession(entries []sessionEntry) sessionState {
	var state sessionState
	var pending []ToolCall
	held := map[string]string{}
	targets := map[string]string{}

	resolve := func(callID, output string) bool {
		if held == nil {
			held = map[string]string{}
		}
		for _, call := range pending {
			if call.ID == callID {
				if _, exists := held[callID]; exists {
					return true
				}
				held[callID] = output
				return true
			}
		}
		return false
	}
	drain := func(interrupt bool) {
		for _, call := range pending {
			output, ok := held[call.ID]
			if !ok {
				if !interrupt {
					break
				}
				output = interruptedToolOutput
				if target := targets[call.ID]; target != "" {
					output = fmt.Sprintf("error: tool call interrupted before its result was recorded (target: %s): its side effects are unknown.", target)
				}
				state.Repaired++
			}
			state.Messages = append(state.Messages, Message{Role: "tool", Content: output, ToolCallID: call.ID})
			delete(held, call.ID)
		}
		pending = nil
		held = map[string]string{}
	}
	appendMessage := func(msg Message) {
		switch msg.Role {
		case "assistant":
			if len(pending) > 0 {
				drain(true)
			}
			copied := msg
			state.Messages = append(state.Messages, copied)
			if len(msg.ToolCalls) > 0 {
				pending = append(pending, msg.ToolCalls...)
			}
		case "tool":
			if resolve(msg.ToolCallID, msg.Content) {
				for len(pending) > 0 {
					call := pending[0]
					output, ok := held[call.ID]
					if !ok {
						break
					}
					state.Messages = append(state.Messages, Message{Role: "tool", Content: output, ToolCallID: call.ID})
					delete(held, call.ID)
					pending = pending[1:]
				}
			}
			// A tool result without a matching call is stale or duplicated;
			// dropping it keeps the history valid for the provider.
		default:
			if len(pending) > 0 {
				drain(true)
			}
			state.Messages = append(state.Messages, msg)
		}
	}

	for _, entry := range entries {
		switch entry.Type {
		case entryCompaction:
			drain(true)
			switch {
			case entry.Upto <= 0:
			case entry.Upto >= len(state.Messages):
				state.Messages = nil
			default:
				state.Messages = append([]Message(nil), state.Messages[entry.Upto:]...)
			}
			// A tool result whose call was summarized away is not a valid
			// message for the provider; drop the orphaned prefix.
			for len(state.Messages) > 0 && state.Messages[0].Role == "tool" {
				state.Messages = state.Messages[1:]
			}
			state.Summary = entry.Summary
		case entryRunStart:
			state.OpenRun = true
			state.LastRunID = entry.RunID
		case entryRunEnd:
			state.OpenRun = false
		case entryMessage:
			if entry.Message != nil {
				appendMessage(*entry.Message)
			}
		case entryToolIntent:
			if entry.Call != nil && entry.Target != "" {
				targets[entry.Call.ID] = entry.Target
			}
		case entryToolResult:
			if entry.Call == nil {
				continue
			}
			if resolve(entry.Call.ID, entry.Output) {
				for len(pending) > 0 {
					call := pending[0]
					output, ok := held[call.ID]
					if !ok {
						break
					}
					state.Messages = append(state.Messages, Message{Role: "tool", Content: output, ToolCallID: call.ID})
					delete(held, call.ID)
					pending = pending[1:]
				}
			}
		case entryAssistantPartial:
			if entry.Message != nil {
				partial := *entry.Message
				state.LastPartial = &partial
			}
		case entryUsage:
			if entry.Usage != nil {
				state.Usage.Input += entry.Usage.Input
				state.Usage.Output += entry.Usage.Output
				state.Usage.CachedInput += entry.Usage.CachedInput
				state.Usage.Reasoning += entry.Usage.Reasoning
			}
		}
	}
	if len(pending) > 0 {
		drain(true)
	}
	return state
}

func parseEntryLine(line string) (sessionEntry, bool) {
	var entry sessionEntry
	if err := json.Unmarshal([]byte(line), &entry); err == nil && entry.Type != "" {
		return entry, true
	}
	var message Message
	if err := json.Unmarshal([]byte(line), &message); err == nil {
		return sessionEntry{Type: entryMessage, Message: &message}, true
	}
	return sessionEntry{}, false
}

// loadPath reads a session log. A torn final line (a crash between write and
// fsync) is ignored; a corrupt line anywhere else is an error.
func loadPath(path string) ([]sessionEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session %s: %w", path, err)
	}
	lines := bytes.Split(data, []byte("\n"))
	endsWithNewline := len(data) == 0 || data[len(data)-1] == '\n'
	var entries []sessionEntry
	for i, raw := range lines {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}
		entry, ok := parseEntryLine(string(trimmed))
		if !ok {
			last := i == len(lines)-1
			if last && !endsWithNewline {
				fmt.Fprintf(os.Stderr, "warning: ignoring truncated session tail in %s\n", path)
				break
			}
			return nil, fmt.Errorf("invalid session %s", path)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// sessionLog appends to a session file. Every append hits the file with
// O_APPEND and fsyncs before returning: once the engine acts on an event
// (starts a tool, sends a request), the record is durable.
type sessionLog struct {
	path string
	file *os.File
	mu   sync.Mutex
}

func openSessionLog(path string) (*sessionLog, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir %s: %w", dir, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", path, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock session %s: %w", path, err)
	}
	syncDir(dir)
	return &sessionLog{path: path, file: file}, nil
}

func (l *sessionLog) append(entry sessionEntry) error {
	if entry.At == 0 {
		entry.At = time.Now().UnixMilli()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode session %s: %w", l.path, err)
	}
	data = append(data, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(data); err != nil {
		return fmt.Errorf("write session %s: %w", l.path, err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("sync session %s: %w", l.path, err)
	}
	return nil
}

func (l *sessionLog) close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	unlockFile(l.file)
	return l.file.Close()
}
