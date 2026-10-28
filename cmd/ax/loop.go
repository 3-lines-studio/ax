package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxRetries = 2

func runStream(ctx context.Context, provider Provider, opts *runOptions, msgs []Message, cancel *atomic.Bool, sink runSink) runEnd {
	history := append([]Message(nil), msgs...)
	usage := Usage{}
	lastPrompt := 0
	compactedAt := -1
	for turn := 0; turn < opts.maxTurns; turn++ {
		if cancel.Load() {
			return runEnd{messages: history, usage: usage, outcome: outcomeCancelled}
		}
		history = drainSteering(opts.steer, history, sink)
		if opts.compact != nil && len(history) > compactedAt {
			if threshold := compactThreshold(opts.compactAt, opts.contextWindow); threshold > 0 &&
				(lastPrompt > threshold || estimateTokens(opts.system, history) > threshold) {
				next, system, err := opts.compact(ctx, history, turn > 0)
				if err != nil {
					return runEnd{messages: history, usage: usage, outcome: outcomeFailed, failMsg: err.Error()}
				}
				history, opts.system = next, system
				lastPrompt = 0
				compactedAt = len(history)
			}
		}
		resp, calls, err := requestStream(provider, opts, history, cancel, sink)
		if err != nil {
			if cancel.Load() {
				return runEnd{messages: history, usage: usage, outcome: outcomeCancelled}
			}
			return runEnd{messages: history, usage: usage, outcome: outcomeFailed, failMsg: err.Error()}
		}
		lastPrompt = resp.Usage.Input + resp.Usage.Output
		usage = Usage{
			Input:       usage.Input + resp.Usage.Input,
			Output:      usage.Output + resp.Usage.Output,
			CachedInput: usage.CachedInput + resp.Usage.CachedInput,
		}
		history = append(history, resp.Message)
		sink.assistant(turn, history[len(history)-1], resp.Usage)
		sink.assistantDone()
		if len(calls) == 0 {
			switch resp.StopReason {
			case "length":
				return runEnd{messages: history, usage: usage, outcome: outcomeFailed, failMsg: "the model hit the output token limit before finishing; raise -max-tokens or continue the prompt"}
			case "content_filter":
				return runEnd{messages: history, usage: usage, outcome: outcomeFailed, failMsg: "the provider stopped the response with finish_reason content_filter"}
			case "insufficient_system_resource", "aborted":
				return runEnd{messages: history, usage: usage, outcome: outcomeFailed, failMsg: "the provider interrupted the response with finish_reason " + resp.StopReason}
			default:
				return runEnd{messages: history, usage: usage, outcome: outcomeDone}
			}
		}
		truncated := resp.StopReason == "length" || resp.StopReason == "insufficient_system_resource"
		if !runToolBatch(opts.tools, calls, truncated, turn, cancel, sink, &history) {
			return runEnd{messages: history, usage: usage, outcome: outcomeCancelled}
		}
	}
	return runEnd{messages: history, usage: usage, outcome: outcomeMaxTurns}
}

// drainSteering appends every message typed while the previous turn ran. A
// steered message is a normal user turn: it goes into the history the model
// sees and into the session log through the sink.
func drainSteering(steer <-chan string, history []Message, sink runSink) []Message {
	if steer == nil {
		return history
	}
	for {
		select {
		case text, ok := <-steer:
			if !ok {
				return history
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			message := Message{Role: "user", Content: text}
			history = append(history, message)
			sink.steer(message)
		default:
			return history
		}
	}
}

func requestStream(provider Provider, opts *runOptions, history []Message, cancel *atomic.Bool, sink runSink) (Response, []ToolCall, error) {
	req := &Request{Model: opts.model, System: opts.system, Messages: history, Tools: opts.tools}
	attempt := 0
	for {
		handle := provider.stream(req, cancel)
		var calls []ToolCall
		forwarded := 0
		for ev := range handle.Events() {
			switch ev.Kind {
			case eventContent:
				forwarded++
				sink.assistantDelta(ev.Text)
			case eventReasoning:
				forwarded++
				sink.reasoningDelta(ev.Reasoning)
			case eventToolCall:
				forwarded++
				if ev.Call != nil {
					calls = append(calls, *ev.Call)
				}
			case eventTokens:
				sink.tokens(ev.Usage.Input, ev.Usage.Output, ev.Usage.CachedInput)
			case eventDone:
			}
		}
		resp, err := handle.Join()
		if err == nil {
			switch resp.StopReason {
			case "insufficient_system_resource", "aborted":
				err = newProvider("the provider interrupted the response with finish_reason " + resp.StopReason)
			case "content_filter":
				return Response{}, calls, newProvider("the provider stopped the response with finish_reason content_filter")
			default:
				return resp, calls, nil
			}
		}
		retryableErr := retryable(err)
		if resp.StopReason == "insufficient_system_resource" || resp.StopReason == "aborted" {
			retryableErr = true
		}
		if attempt >= maxRetries || forwarded > 0 || !retryableErr {
			return Response{}, calls, err
		}
		attempt++
		delay := backoff(attempt)
		var axErr *axError
		if errors.As(err, &axErr) && axErr.retryAfter > 0 {
			delay = int(axErr.retryAfter / time.Millisecond)
		}
		if err := sleepWithCancel(delay, cancel); err != nil {
			return Response{}, calls, err
		}
	}
}

func pushToolResult(h *[]Message, call ToolCall, content string) {
	*h = append(*h, Message{Role: "tool", Content: content, ToolCallID: call.ID})
}

func runTool(ctx context.Context, tools []Tool, call ToolCall, progress func(string)) string {
	for _, t := range tools {
		if t.Name != call.Name {
			continue
		}
		if err := validateToolArguments(t, call.Arguments); err != nil {
			return "error: " + err.Error()
		}
		timeout := t.Timeout
		if timeout <= 0 {
			timeout = defaultToolTimeout
		}
		runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		if t.RunContext != nil {
			return t.RunContext(runCtx, call.Arguments, progress)
		}
		if t.Run != nil {
			return t.Run(call.Arguments, progress)
		}
		return fmt.Sprintf("error: tool %s has no implementation", call.Name)
	}
	return fmt.Sprintf("error: unknown tool: %s", call.Name)
}

func execTool(ctx context.Context, tools []Tool, call ToolCall, sink runSink) string {
	return runTool(ctx, tools, call, func(text string) { sink.toolDelta(call, text) })
}

// toolRunContext turns the engine's cancel flag into a context so running
// tool processes are signalled instead of being left behind.
func toolRunContext(cancel *atomic.Bool) (context.Context, context.CancelFunc) {
	ctx, cancelFn := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if cancel.Load() {
					cancelFn()
					return
				}
			}
		}
	}()
	return ctx, cancelFn
}

func runToolBatch(tools []Tool, calls []ToolCall, truncated bool, turn int, cancel *atomic.Bool, sink runSink, h *[]Message) bool {
	anySequential := false
	for _, c := range calls {
		for _, t := range tools {
			if t.Name == c.Name && t.Sequential {
				anySequential = true
			}
		}
	}
	ctx, cancelTools := toolRunContext(cancel)
	defer cancelTools()
	if truncated || anySequential || len(calls) <= 1 {
		interrupted := false
		for _, call := range calls {
			interrupted = interrupted || cancel.Load()
			sink.toolStart(call)
			output := ""
			switch {
			case interrupted:
				output = "error: tool call not executed: the run was interrupted."
			case truncated:
				output = "error: tool call not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments."
			default:
				output = execTool(ctx, tools, call, sink)
			}
			sink.toolResult(call)
			pushToolResult(h, call, output)
			sink.tool(turn, (*h)[len(*h)-1])
		}
		return !cancel.Load()
	}
	return runParallel(ctx, tools, calls, turn, cancel, sink, h)
}

type parallelMsg struct {
	kind   int
	idx    int
	text   string
	output string
}

func runParallel(ctx context.Context, tools []Tool, calls []ToolCall, turn int, cancel *atomic.Bool, sink runSink, h *[]Message) bool {
	for _, call := range calls {
		sink.toolStart(call)
	}
	outputs := make([]string, len(calls))
	ch := make(chan parallelMsg, 256)
	var wg sync.WaitGroup
	for idx, call := range calls {
		wg.Add(1)
		go func(idx int, call ToolCall) {
			defer wg.Done()
			if cancel.Load() {
				ch <- parallelMsg{kind: 1, idx: idx, output: "error: tool call not executed: the run was interrupted."}
				return
			}
			output := runTool(ctx, tools, call, func(text string) {
				ch <- parallelMsg{kind: 0, idx: idx, text: text}
			})
			ch <- parallelMsg{kind: 1, idx: idx, output: output}
		}(idx, call)
	}
	go func() { wg.Wait(); close(ch) }()
	for msg := range ch {
		if msg.kind == 0 {
			if msg.idx < len(calls) {
				sink.toolDelta(calls[msg.idx], msg.text)
			}
			continue
		}
		outputs[msg.idx] = msg.output
	}
	for idx, call := range calls {
		sink.toolResult(call)
		content := outputs[idx]
		if content == "" {
			content = "error: tool thread panicked"
		}
		pushToolResult(h, call, content)
		sink.tool(turn, (*h)[len(*h)-1])
	}
	return !cancel.Load()
}

func backoff(attempt int) int {
	shift := attempt - 1
	if shift > 4 {
		shift = 4
	}
	base := 500 << shift
	if base > 8000 {
		base = 8000
	}
	now := int(time.Now().UnixNano())
	return base - now%(base/4+1)
}

func sleepWithCancel(ms int, cancel *atomic.Bool) error {
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(deadline) {
		if cancel.Load() {
			return newProvider("interrupted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}
