package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3-lines-studio/ax/internal/axproto"
)

// ax streams one JSON object per line and does not cap the line length, so a
// tool result can be large. Cap the scanner at 16 MiB: enough headroom for
// normal output, small enough that a rogue line cannot exhaust memory.
const maxEventLine = 16 * 1024 * 1024

// axEvent is the loosely-typed shape of one line from `ax --events`, matching
// the events emitted by cmd/ax/main.go's eventSink.
type axEvent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Outcome   string          `json:"outcome"`
	Message   string          `json:"message"`
}

// runAx spawns one `ax --events` turn, streams its JSONL output as ACP
// session/update notifications, and returns the terminal PromptResponse.
//
// ax event -> ACP action:
//
//	assistant_delta                -> AgentMessageChunk
//	tool_start                     -> ToolCall (pending)
//	tool_delta                     -> ToolCallUpdate (in_progress, accumulated text)
//	tool_result (id+output)        -> ToolCallUpdate (completed)
//	done, outcome=done|compact     -> PromptResponse{StopReason: end_turn}
//	done, outcome=cancelled        -> PromptResponse{StopReason: cancelled}
//	done, outcome=max_turns        -> PromptResponse{StopReason: max_turn_requests}
//	done, outcome=failed           -> JSON-RPC error (ACP has no "failed" StopReason)
//
// Note on a naming quirk in ax's sink: the method named toolResult emits wire
// event "tool_done" (id only), while the method named tool emits
// "tool_result" (id and output). We only act on wire type "tool_result" below,
// since "tool_done" carries nothing it doesn't already give us -- see
// cmd/ax/main.go's eventSink.
func runAx(ctx context.Context, conn *acp.AgentSideConnection, cfg *Config, sess *session, promptText string) (acp.PromptResponse, error) {
	args := []string{
		"-C", sess.cwd,
		"-system", cfg.System,
		"-model", cfg.Model,
		"-base", cfg.Base,
		"--session", sess.axSession,
		"--events",
		promptText,
	}
	// Deliberately not exec.CommandContext: the SDK cancels a Prompt call's
	// ctx as part of dispatching the matching session/cancel notification,
	// which would make Go SIGKILL this process immediately -- racing (and
	// beating) the graceful {"type":"cancel"} stdin write agent.Cancel sends
	// below. ax's own cancel handling is what should end this process; ctx
	// here is only for the SessionUpdate calls below, not process lifetime.
	cmd := exec.Command(cfg.AxPath, args...)
	cmd.Env = os.Environ()
	if len(sess.mcpServers) > 0 {
		toolsFile, err := writeMcpServersFile(sess)
		if err != nil {
			return acp.PromptResponse{}, fmt.Errorf("acpi: writing mcp servers file: %w", err)
		}
		cmd.Env = append(cmd.Env,
			"AX_TOOLS="+strings.TrimSpace(os.Getenv("AX_TOOLS")+" mcpx"),
			"MCPX_SERVERS_FILE="+toolsFile,
		)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return acp.PromptResponse{}, fmt.Errorf("acpi: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return acp.PromptResponse{}, fmt.Errorf("acpi: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return acp.PromptResponse{}, fmt.Errorf("acpi: starting ax: %w", err)
	}

	sess.mu.Lock()
	sess.active = &activeRun{cmd: cmd, stdin: stdin}
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.active = nil
		sess.mu.Unlock()
		_ = stdin.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxEventLine)

	toolContent := map[string]*strings.Builder{} // accumulated tool_delta text, keyed by tool call id
	var (
		errMsg   string
		result   acp.PromptResponse
		terminal bool
		termErr  error
	)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev axEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // ignore malformed/unrecognized lines rather than aborting the turn
		}
		switch ev.Type {
		case axproto.TypeAssistantDelta:
			_ = conn.SessionUpdate(ctx, acp.SessionNotification{
				SessionId: sess.id,
				Update:    acp.UpdateAgentMessageText(ev.Text),
			})

		case axproto.TypeToolStart:
			toolContent[ev.ID] = &strings.Builder{}
			_ = conn.SessionUpdate(ctx, acp.SessionNotification{
				SessionId: sess.id,
				Update: acp.StartToolCall(acp.ToolCallId(ev.ID), ev.Name,
					acp.WithStartKind(guessToolKind(ev.Name)),
					acp.WithStartStatus(acp.ToolCallStatusPending),
					acp.WithStartRawInput(rawJSON(ev.Arguments)),
				),
			})

		case axproto.TypeToolDelta:
			b, ok := toolContent[ev.ID]
			if !ok {
				b = &strings.Builder{}
				toolContent[ev.ID] = b
			}
			b.WriteString(ev.Text)
			_ = conn.SessionUpdate(ctx, acp.SessionNotification{
				SessionId: sess.id,
				Update: acp.UpdateToolCall(acp.ToolCallId(ev.ID),
					acp.WithUpdateStatus(acp.ToolCallStatusInProgress),
					acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(b.String()))}),
				),
			})

		case axproto.TypeToolResult:
			out := outputText(ev.Output)
			_ = conn.SessionUpdate(ctx, acp.SessionNotification{
				SessionId: sess.id,
				Update: acp.UpdateToolCall(acp.ToolCallId(ev.ID),
					acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
					acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(out))}),
					acp.WithUpdateRawOutput(rawJSON(ev.Output)),
				),
			})
			delete(toolContent, ev.ID)

		case axproto.TypeError:
			errMsg = ev.Message

		case axproto.TypeDone:
			terminal = true
			result, termErr = outcomeToResponse(ev.Outcome, errMsg)

		// "protocol", "message", "result", "tool_done", "usage": no ACP
		// action needed for v1 -- see the doc comment above.
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return acp.PromptResponse{}, fmt.Errorf("acpi: reading ax events: %w", err)
	}

	waitErr := cmd.Wait()
	if !terminal {
		if waitErr != nil {
			return acp.PromptResponse{}, fmt.Errorf("acpi: ax exited without a done event: %w", waitErr)
		}
		return acp.PromptResponse{}, fmt.Errorf("acpi: ax exited without a done event")
	}
	if termErr != nil {
		return acp.PromptResponse{}, termErr
	}
	return result, nil
}

func outcomeToResponse(outcome, errMsg string) (acp.PromptResponse, error) {
	switch outcome {
	case axproto.OutcomeDone, axproto.OutcomeCompact:
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	case axproto.OutcomeCancelled:
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case axproto.OutcomeMaxTurns:
		return acp.PromptResponse{StopReason: acp.StopReasonMaxTurnRequests}, nil
	case axproto.OutcomeFailed:
		if errMsg == "" {
			errMsg = "ax run failed"
		}
		return acp.PromptResponse{}, fmt.Errorf("ax: %s", errMsg)
	default:
		return acp.PromptResponse{}, fmt.Errorf("acpi: unknown ax outcome %q", outcome)
	}
}

// guessToolKind maps an ax tool name to an ACP ToolKind hint for client UI.
// ax's Unix tool convention doesn't report a kind, so this is a best-effort
// guess from the name; unmatched names fall back to ToolKindOther.
func guessToolKind(name string) acp.ToolKind {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "read"):
		return acp.ToolKindRead
	case strings.Contains(n, "write"), strings.Contains(n, "edit"):
		return acp.ToolKindEdit
	case strings.Contains(n, "delete"), strings.Contains(n, "remove"):
		return acp.ToolKindDelete
	case strings.Contains(n, "move"), strings.Contains(n, "rename"):
		return acp.ToolKindMove
	case strings.Contains(n, "search"), strings.Contains(n, "grep"), strings.Contains(n, "find"):
		return acp.ToolKindSearch
	case strings.Contains(n, "bash"), strings.Contains(n, "exec"), strings.Contains(n, "run"):
		return acp.ToolKindExecute
	case strings.Contains(n, "fetch"), strings.Contains(n, "http"), strings.Contains(n, "web"):
		return acp.ToolKindFetch
	default:
		return acp.ToolKindOther
	}
}

// rawJSON decodes a json.RawMessage into a plain any so it round-trips
// through the SDK's RawInput/RawOutput (typed any) fields untouched.
func rawJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

// outputText extracts tool output text. ax's tool_result "output" field
// mirrors Message.content, which is a plain string in ax's model.
func outputText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// writeMcpServersFile writes a session's ACP-provided MCP servers to a JSON
// file next to its ax session file. Nothing reads this file yet -- it's the
// contract mcpx (not yet built) is expected to consume via MCPX_SERVERS_FILE.
func writeMcpServersFile(sess *session) (string, error) {
	data, err := json.Marshal(sess.mcpServers)
	if err != nil {
		return "", err
	}
	path := sess.axSession + ".mcp-servers.json"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
