package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/3-lines-studio/ax/internal/axproto"
)

// Context compaction follows pixi's Auto-compact: when the projected prompt
// crosses a fraction of the context window, the older half of the
// conversation is summarized into the system prompt and the session log
// records the cutoff. The log stays append-only; the summary is one more
// entry.
const (
	// compactSummaryTokens bounds the summarizer's own output.
	compactSummaryTokens = 8000
	// compactTranscriptMax caps what the summarizer reads.
	compactTranscriptMax  = 700_000
	compactTranscriptHead = 150_000
	// compactToolResultChars keeps tool output out of the transcript budget.
	compactToolResultChars = 2000

	// Defaults copied from pixi: compact at half a one-million-token window.
	defaultCompactAt     = 0.5
	defaultContextWindow = 1_000_000
)

// continueNote gives the model a user turn to act on after a mid-run
// compaction swallowed the current one.
const continueNote = "Continue the task. The earlier conversation was compacted into the summary in the system prompt; that summary is accurate. Do not repeat work that is already done."

const compactSystemPrompt = `You compress conversations between a user and an AI coding assistant into a faithful working summary. The summary replaces the original transcript in the assistant's context, so it must preserve everything needed to continue the work without re-reading the conversation.`

func compactUserPrompt(transcript string) string {
	return `Summarize the following conversation for an AI coding assistant that must continue the work afterwards.

Rules:
- Write factual, dense prose in sections: Goal, Decisions, Changes made (with file paths), Current state, Open tasks, Important commands, Facts and preferences stated by the user.
- Keep exact file paths, command lines, error messages and identifiers that matter.
- Do not invent anything. Do not include tool-call transcripts verbatim; compress them into findings.
- Keep it under 3000 words.

CONVERSATION:
` + transcript
}

// summarySection is appended to the system prompt after a compaction.
func summarySection(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	return "\n\n## Summary of the earlier conversation\n" +
		"The messages before this summary were compacted to save context. Treat this summary as accurate history and continue from it.\n\n" +
		summary + "\n"
}

// compactThreshold is the prompt size, in tokens, that triggers compaction.
// A zero or negative setting disables it.
func compactThreshold(compactAt float64, contextWindow int) int {
	if compactAt <= 0 || contextWindow <= 0 {
		return 0
	}
	return int(float64(contextWindow) * compactAt)
}

// estimateTokens approximates a prompt. There is no tokenizer in the binary;
// chars/3 errs high for prose, which is the direction that compacts early.
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
		chars += len(msg.Content) + 16
		for _, call := range msg.ToolCalls {
			chars += len(call.Name) + len(call.Arguments) + 16
		}
	}
	return chars/3 + images*1024
}

// renderTranscript turns the history into the summarizer's input. Tool output
// is compressed to findings; the current summary is prepended so a second
// compaction keeps what the first one learned.
func renderTranscript(summary string, msgs []Message) string {
	var out strings.Builder
	if summary = strings.TrimSpace(summary); summary != "" {
		out.WriteString("SUMMARY OF EVEN EARLIER CONVERSATION:\n")
		out.WriteString(summary)
		out.WriteString("\n\n")
	}
	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			continue
		case "user":
			out.WriteString("USER: ")
			out.WriteString(msg.Text())
			out.WriteString("\n\n")
		case "assistant":
			if text := strings.TrimSpace(msg.Text()); text != "" {
				out.WriteString("ASSISTANT: ")
				out.WriteString(text)
				out.WriteString("\n\n")
			}
			for _, call := range msg.ToolCalls {
				out.WriteString("TOOL CALL ")
				out.WriteString(call.Name)
				out.WriteString("(")
				out.WriteString(truncateText(call.Arguments, 400))
				out.WriteString(")\n")
			}
		case "tool":
			out.WriteString("TOOL RESULT: ")
			out.WriteString(truncateText(msg.Content, compactToolResultChars))
			out.WriteString("\n\n")
		}
	}
	return out.String()
}

func truncateText(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// summarizeTranscript asks the provider for a working summary. The caller
// supplies a provider configured with thinking off and a small output budget:
// a summary is prose, not a plan.
func summarizeTranscript(ctx context.Context, provider Provider, model string, summary string, history []Message, cancel *atomic.Bool) (string, error) {
	transcript := renderTranscript(summary, history)
	if len(transcript) > compactTranscriptMax {
		head := compactTranscriptHead
		tail := compactTranscriptMax - head
		transcript = transcript[:head] + "\n\n[... middle of the conversation omitted ...]\n\n" + transcript[len(transcript)-tail:]
	}
	if cancel == nil {
		cancel = &atomic.Bool{}
	}
	handle := provider.stream(&Request{
		Model:    model,
		System:   compactSystemPrompt,
		Messages: []Message{{Role: "user", Content: compactUserPrompt(transcript)}},
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

// compactor records compactions in the session log and rebuilds the prompt.
type compactor struct {
	provider Provider
	model    string
	base     string // system prompt without the summary section
	summary  string // summary currently in the prompt
	log      *sessionLog
	runID    string
	cancel   *atomic.Bool
	emit     func(any)
}

// compact summarizes the projected history and returns what the next request
// should send. midRun keeps a user turn (the continue note) so the model has
// something to act on; before the first turn the pending user message is kept
// instead.
func (c *compactor) compact(ctx context.Context, history []Message, midRun bool) ([]Message, string, error) {
	ctx, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	summary, err := summarizeTranscript(ctx, c.provider, c.model, c.summary, history, c.cancel)
	if err != nil {
		return nil, "", fmt.Errorf("context is over the compact_at threshold and compaction failed: %w", err)
	}
	kept := history
	upto := 0
	if midRun {
		kept = []Message{{Role: "user", Content: continueNote}}
		upto = len(history)
	} else if index := lastUserIndex(history); index > 0 {
		kept = history[index:]
		upto = index
	}
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
	return append([]Message(nil), kept...), c.base + summarySection(summary), nil
}

func lastUserIndex(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}
