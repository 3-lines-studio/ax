package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const maxRetries = 2

func runStream(provider Provider, opts *runOptions, msgs []Message, cancel *atomic.Bool, sink runSink) runEnd {
	history := append([]Message(nil), msgs...)
	usage := Usage{}
	for turn := 0; turn < opts.maxTurns; turn++ {
		if cancel.Load() {
			return runEnd{messages: history, usage: usage, outcome: outcomeCancelled}
		}
		resp, calls, err := requestStream(provider, opts, history, cancel, sink)
		if err != nil {
			if cancel.Load() {
				return runEnd{messages: history, usage: usage, outcome: outcomeCancelled}
			}
			return runEnd{messages: history, usage: usage, outcome: outcomeFailed, failMsg: err.Error()}
		}
		usage = Usage{
			Input:       usage.Input + resp.Usage.Input,
			Output:      usage.Output + resp.Usage.Output,
			CachedInput: usage.CachedInput + resp.Usage.CachedInput,
		}
		history = append(history, resp.Message)
		sink.assistant(turn, history[len(history)-1], resp.Usage)
		sink.assistantDone()
		if len(calls) == 0 {
			return runEnd{messages: history, usage: usage, outcome: outcomeDone}
		}
		truncated := resp.StopReason == "length"
		if !runToolBatch(opts.tools, calls, truncated, turn, cancel, sink, &history) {
			return runEnd{messages: history, usage: usage, outcome: outcomeCancelled}
		}
		if sink.shouldCompact(resp.Usage.Input, resp.Usage.Output) {
			return runEnd{messages: history, usage: usage, outcome: outcomeCompact}
		}
	}
	return runEnd{messages: history, usage: usage, outcome: outcomeMaxTurns}
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
			return resp, calls, nil
		}
		if attempt >= maxRetries || forwarded > 0 || !retryable(err) {
			return Response{}, calls, err
		}
		attempt++
		if err := sleepWithCancel(backoff(attempt), cancel); err != nil {
			return Response{}, calls, err
		}
	}
}

func pushToolResult(h *[]Message, call ToolCall, content string) {
	*h = append(*h, Message{Role: "tool", Content: content, ToolCallID: call.ID})
}

func runTool(tools []Tool, call ToolCall, progress func(string)) string {
	for _, t := range tools {
		if t.Name == call.Name {
			return t.Run(call.Arguments, progress)
		}
	}
	return fmt.Sprintf("error: unknown tool: %s", call.Name)
}

func execTool(tools []Tool, call ToolCall, sink runSink) string {
	return runTool(tools, call, func(text string) { sink.toolDelta(call, text) })
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
				output = execTool(tools, call, sink)
			}
			sink.toolResult(call)
			pushToolResult(h, call, output)
			sink.tool(turn, (*h)[len(*h)-1])
		}
		return !cancel.Load()
	}
	return runParallel(tools, calls, turn, cancel, sink, h)
}

type parallelMsg struct {
	kind   int
	idx    int
	text   string
	output string
}

func runParallel(tools []Tool, calls []ToolCall, turn int, cancel *atomic.Bool, sink runSink, h *[]Message) bool {
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
			output := runTool(tools, call, func(text string) {
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
