package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/3-lines-studio/ax/internal/axproto"
)

func runCompactCommand(cfg cliConfig, fc fileConfig, bot *botCtx, args []string) {
	path := cfg.session
	dryRun := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session", "-session":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --session needs a file")
				os.Exit(2)
			}
			path = args[i]
		case "--dry-run", "-dry-run", "--print":
			dryRun = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax compact --session FILE [--dry-run]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown compact flag %q\n", args[i])
			os.Exit(2)
		}
	}
	if path == "" {
		fmt.Fprint(os.Stderr, "usage: ax compact --session FILE [--dry-run]\n")
		os.Exit(2)
	}
	entries, err := loadPath(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	state := deriveSession(entries)
	if len(state.Messages) == 0 {
		fmt.Fprintln(os.Stderr, "error: nothing to compact yet")
		os.Exit(1)
	}
	summarizer := NewOpenAI(cfg.base, apiKey(fc, bot))
	summarizer.SessionPath = path
	thinking := false
	summarizer.Thinking = &thinking
	summarizer.Effort = "none"
	summarizer.MaxTokens = compactSummaryTokens

	provider := Summarizer{provider: summarizer, model: cfg.model, cancel: &atomic.Bool{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	upto := len(state.Messages)
	if index := lastUserIndex(state.Messages); index > 0 {
		upto = index
	}
	window := contextWindowFor(cfg, fc)
	if window > 0 && estimateTokens("", state.Messages) > window {
		window = estimateTokens("", state.Messages)
	}
	summary, err := provider.summarize(ctx, state.Messages[:upto], state.Summary, "", window)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if !dryRun {
		log, err := openSessionLog(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		err = log.append(sessionEntry{Type: entryCompaction, Upto: upto, Summary: summary})
		closeErr := log.close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if closeErr != nil {
			fmt.Fprintln(os.Stderr, "error:", closeErr)
			os.Exit(1)
		}
	}
	printJSON(map[string]any{
		"type":     axproto.TypeCompaction,
		"upto":     upto,
		"summary":  summary,
		"messages": len(state.Messages),
		"dry_run":  dryRun,
	})
	printJSON(map[string]any{"type": axproto.TypeDone, "outcome": axproto.OutcomeCompact})
}
