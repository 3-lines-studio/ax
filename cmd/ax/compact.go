package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/3-lines-studio/ax/internal/axproto"
)

const (
	compactSummaryTokens   = 8000
	compactTranscriptMax   = 700_000
	compactWindowMargin    = 50
	compactTranscriptHead  = 150_000
	compactToolResultChars = 4000
	compactOmitToolChars   = 2000
	compactEstimateDivisor = 4

	defaultCompactAt     = 0.5
	defaultContextWindow = 1_000_000
)

const continueNote = "Continue the task. The earlier conversation was compacted into the summary in the system prompt; that summary is accurate. Do not repeat work that is already done."

const compactSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const compactSummaryPrompt = `<conversation>
`

const compactSummaryFormat = `
</conversation>

Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const compactUpdateFormat = `
</conversation>

<previous-summary>
`

const compactUpdateRules = `
</previous-summary>

The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const compactTurnPrefixPrompt = `The messages above are earlier context from an ongoing conversation. Later messages are stored separately and do not need to be reconstructed.

Create a concise checkpoint of the user's request and the progress shown above. This checkpoint will be placed before the later messages so the conversation can continue with the necessary context.

## Original Request
[What did the user ask for?]

## Progress So Far
- [Key decisions and work completed in these messages]

## Context Needed to Continue
- [Information from these messages needed to understand the later work]

Only summarize information explicitly present above. Do not infer or recreate later messages.`

func summarySection(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	return "\n\n## Summary of the earlier conversation\n" +
		"The messages before this summary were compacted to save context. Treat this summary as accurate history and continue from it.\n\n" +
		summary + "\n"
}

func compactThreshold(compactAt float64, contextWindow int) int {
	if compactAt <= 0 || contextWindow <= 0 {
		return 0
	}
	return int(float64(contextWindow) * compactAt)
}

func estimateTokens(system string, msgs []Message) int {
	chars := len(system)
	images := 0
	for _, msg := range msgs {
		for _, part := range msg.ContentParts {
			if part.Image != nil {
				images++
				continue
			}
			chars += len(part.Text)
		}
		chars += len(msg.Content) + len(msg.ReasoningContent) + 16
		for _, call := range msg.ToolCalls {
			chars += len(call.Name) + len(call.Arguments) + 16
		}
	}
	return chars/compactEstimateDivisor + images*1024
}

func compactMessageText(msg Message) string {
	if text := msg.Text(); text != "" {
		return text
	}
	return msg.Content
}

const compactFileToolPrefixes = "read,edit,read_file,write,write_file,edit_file,patch,apply_patch,multiedit,str_replace"

func compactToolPaths(call ToolCall) (path string, write bool) {
	var args struct {
		Path     string `json:"path"`
		File     string `json:"file_path"`
		Filename string `json:"filename"`
		Filepath string `json:"filepath"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return "", false
	}
	path = firstNonEmpty(args.Path, args.File, args.Filename, args.Filepath)
	if path == "" {
		return "", false
	}
	prefixes := strings.Split(compactFileToolPrefixes, ",")
	last := call.Name
	if index := strings.LastIndex(last, "."); index >= 0 {
		last = last[index+1:]
	}
	for _, prefix := range prefixes {
		if !strings.HasPrefix(last, prefix) {
			continue
		}
		for _, writer := range []string{"write", "edit", "patch", "apply", "str_replace"} {
			if strings.HasPrefix(last, writer) {
				return path, true
			}
		}
		return path, false
	}
	return "", false
}

func compactFileLists(msgs []Message) (read []string, modified []string) {
	reads := map[string]bool{}
	writes := map[string]bool{}
	for _, msg := range msgs {
		for _, call := range msg.ToolCalls {
			path, write := compactToolPaths(call)
			if path == "" {
				continue
			}
			if write {
				writes[path] = true
			} else {
				reads[path] = true
			}
		}
	}
	for path := range writes {
		delete(reads, path)
	}
	for path := range reads {
		read = append(read, path)
	}
	for path := range writes {
		modified = append(modified, path)
	}
	sort.Strings(read)
	sort.Strings(modified)
	return read, modified
}

func compactFilesSection(read, modified []string) string {
	var sections []string
	if len(read) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(read, "\n")+"\n</read-files>")
	}
	if len(modified) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modified, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

func renderTranscript(msgs []Message) string {
	var parts []string
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			if text := compactMessageText(msg); text != "" {
				parts = append(parts, "[User]: "+text)
			}
		case "assistant":
			if text := strings.TrimSpace(msg.Text()); text != "" {
				parts = append(parts, "[Assistant]: "+text)
			}
			if len(msg.ToolCalls) > 0 {
				calls := make([]string, 0, len(msg.ToolCalls))
				for _, call := range msg.ToolCalls {
					calls = append(calls, call.Name+"("+call.Arguments+")")
				}
				parts = append(parts, "[Assistant tool calls]: "+truncateText(strings.Join(calls, "; "), compactToolResultChars))
			}
		case "tool":
			if content := strings.TrimSpace(msg.Content); content != "" {
				parts = append(parts, "[Tool result]: "+truncateText(content, compactToolResultChars))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func truncateText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + fmt.Sprintf("\n\n[... %d more characters truncated]", len(value)-limit)
}

func compactPrompt(transcript, previous, tail string) string {
	var out strings.Builder
	out.WriteString(compactSummaryPrompt)
	out.WriteString(transcript)
	if previous = strings.TrimSpace(previous); previous != "" {
		out.WriteString(compactUpdateFormat)
		out.WriteString(previous)
		out.WriteString(compactUpdateRules)
	} else {
		out.WriteString(compactSummaryFormat)
	}
	out.WriteString(tail)
	return out.String()
}

func transcriptBudget(window int) int {
	if window <= 0 {
		return 0
	}
	budget := (window - compactWindowMargin - 2*compactSummaryTokens) * compactEstimateDivisor
	if budget < compactTranscriptHead {
		budget = compactTranscriptHead
	}
	return budget
}

func splitTranscript(transcript string, budget int) []string {
	if budget <= 0 || len(transcript) <= budget {
		return []string{transcript}
	}
	lines := strings.Split(transcript, "\n\n")
	var chunks []string
	var current strings.Builder
	for _, line := range lines {
		for len(line) > budget {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			chunks = append(chunks, line[:budget])
			line = line[budget:]
		}
		if len(line) == 0 {
			continue
		}
		if current.Len() > 0 && current.Len()+len(line)+2 > budget {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	if len(chunks) == 0 {
		return []string{transcript}
	}
	return chunks
}

type Summarizer struct {
	provider Provider
	model    string
	cancel   *atomic.Bool
}

func (s *Summarizer) summarize(ctx context.Context, msgs []Message, previous, tail string, window int) (string, error) {
	if len(msgs) == 0 {
		return previous, nil
	}
	chunks := splitTranscript(renderTranscript(msgs), transcriptBudget(window))
	for _, chunk := range chunks {
		text, err := s.request(ctx, compactPrompt(chunk, previous, tail))
		if err != nil {
			return "", err
		}
		previous = text
	}
	return previous, nil
}

func (s *Summarizer) request(ctx context.Context, prompt string) (string, error) {
	cancel := s.cancel
	if cancel == nil {
		cancel = &atomic.Bool{}
	}
	handle := s.provider.stream(&Request{
		Model:    s.model,
		System:   compactSystemPrompt,
		Messages: []Message{{Role: "user", Content: prompt}},
	}, cancel)
	var content strings.Builder
	for ev := range handle.Events() {
		if ev.Kind == eventContent {
			content.WriteString(ev.Text)
		}
	}
	if _, err := handle.Join(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	out := strings.TrimSpace(content.String())
	if out == "" {
		return "", errors.New("the summarizer returned an empty summary")
	}
	return out, nil
}

type compactor struct {
	provider Provider
	model    string
	base     string
	volatile string
	window   int
	context  int
	tail     string
	summary  string
	log      *sessionLog
	runID    string
	cancel   *atomic.Bool
	emit     func(any)
}

func (c *compactor) compact(ctx context.Context, history []Message, midRun bool) ([]Message, string, error) {
	ctx, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	window := c.window
	if c.context > window {
		window = c.context
	}
	upto := len(history)
	kept := []Message{{Role: "user", Content: continueNote}}
	if !midRun {
		if index := lastUserIndex(history); index > 0 {
			upto = index
			kept = append([]Message(nil), history[index:]...)
		}
	}
	summary, err := (&Summarizer{
		provider: c.provider, model: c.model, cancel: c.cancel,
	}).summarize(ctx, history[:upto], c.summary, c.tail, window)
	if err != nil {
		note := fmt.Sprintf("Context compaction failed: %v. The full history is kept.", err)
		if ctx.Err() != nil {
			return append([]Message(nil), history...), c.unchanged(), nil
		}
		if c.log != nil {
			if appendErr := c.log.append(sessionEntry{Type: entryMessage, RunID: c.runID, Message: &Message{Role: "user", Content: note}}); appendErr != nil {
				return nil, "", appendErr
			}
		}
		return append([]Message(nil), history...), c.unchanged(), nil
	}
	read, modified := compactFileLists(history[:upto])
	summary += compactFilesSection(read, modified)
	if c.log != nil {
		if err := c.log.append(sessionEntry{Type: entryCompaction, RunID: c.runID, Upto: upto, Summary: summary}); err != nil {
			return nil, "", err
		}
		if midRun {
			note := Message{Role: "user", Content: continueNote}
			if err := c.log.append(sessionEntry{Type: entryMessage, RunID: c.runID, Message: &note}); err != nil {
				return nil, "", err
			}
		}
	}
	c.summary = summary
	if c.emit != nil {
		c.emit(map[string]any{"type": axproto.TypeCompaction, "upto": upto, "summary": summary})
	}
	return append([]Message(nil), kept...), c.system(summary), nil
}

func (c *compactor) system(summary string) string {
	return c.base + summarySection(summary) + c.volatile
}

func (c *compactor) unchanged() string {
	return c.base + summarySection(c.summary) + c.volatile
}

func lastUserIndex(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}
