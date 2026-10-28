package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/3-lines-studio/ax/internal/axproto"
)

const maxOutput = 16 * 1024
const maxDescribeOutput = 64 * 1024
const defaultToolTimeout = 300 // seconds

type externalToolSpec = axproto.ToolSpec

var (
	childGroups sync.Map
	signalOnce  sync.Once
)

func ensureSignalHandler() {
	signalOnce.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			for sig := range ch {
				childGroups.Range(func(key, _ interface{}) bool {
					_ = syscall.Kill(-key.(int), syscall.SIGKILL)
					return true
				})
				if sig == syscall.SIGINT {
					os.Exit(130)
				}
				os.Exit(143)
			}
		}()
	})
}

func registerGroup(pgid int)   { childGroups.Store(pgid, struct{}{}) }
func unregisterGroup(pgid int) { childGroups.Delete(pgid) }

func sanitize(value string) string {
	var out strings.Builder
	for _, r := range value {
		code := r
		if code == 0x09 || code == 0x0a || code == 0x0d {
			out.WriteRune(r)
		} else if code > 0x1f && (code < 0xfff9 || code > 0xfffb) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func tail(value string) string {
	if len(value) <= maxOutput {
		return value
	}
	start := len(value) - maxOutput
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func validToolName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' {
			continue
		}
		return false
	}
	return true
}

// tailBuffer keeps the last limit bytes of a stream without growing with it.
// Tool output can be arbitrarily large; the model only ever sees the tail.
type tailBuffer struct {
	buf   []byte
	limit int
	total int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}
	if len(b.buf)+len(p) > b.limit {
		drop := len(b.buf) + len(p) - b.limit
		copy(b.buf, b.buf[drop:])
		b.buf = b.buf[:len(b.buf)-drop]
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.buf) }

func (b *tailBuffer) Truncated() bool { return b.total > len(b.buf) }

// limitedBuffer accepts at most limit bytes and remembers when it overflowed,
// so discovery can reject an oversized describe stream instead of parsing a
// silent truncation.
type limitedBuffer struct {
	buf      []byte
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.exceeded {
		return len(p), nil
	}
	if len(b.buf)+len(p) > b.limit {
		b.exceeded = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func boundedDescribe(command *exec.Cmd) (int, []byte, []byte, bool, error) {
	ensureSignalHandler()
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxDescribeOutput, maxDescribeOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return -1, nil, nil, false, err
	}
	registerGroup(command.Process.Pid)
	waitErr := command.Wait()
	unregisterGroup(command.Process.Pid)
	exceeded := stdout.exceeded || stderr.exceeded
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), stdout.buf, stderr.buf, exceeded, nil
		}
		return -1, nil, nil, exceeded, waitErr
	}
	return 0, stdout.buf, stderr.buf, exceeded, nil
}

func tryExternalTools(commands string, workdir string) ([]Tool, error) {
	var tools []Tool
	for _, command := range strings.Fields(commands) {
		discovered, err := discoverProvider(context.Background(), command, workdir, nil)
		if err != nil {
			return nil, err
		}
		for _, tool := range discovered {
			for _, existing := range tools {
				if existing.Name == tool.Name {
					return nil, fmt.Errorf("duplicate tool name: %s", tool.Name)
				}
			}
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

// discoverProvider asks a provider for its tool descriptors. A non-nil env is
// the connection's environment: it replaces the inherited one for the child, so
// a remote workspace sees only what its connection configured.
func discoverProvider(ctx context.Context, command, workdir string, env []string) ([]Tool, error) {
	describe := exec.CommandContext(ctx, command, "describe")
	describe.Dir = workdir
	if len(env) > 0 {
		describe.Env = env
	}
	code, stdout, stderr, exceeded, err := boundedDescribe(describe)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("discover tools from %s: %w", command, ctx.Err())
		}
		return nil, fmt.Errorf("discover tools from %s: %w", command, err)
	}
	if exceeded {
		return nil, fmt.Errorf("discover tools from %s: output exceeds %d bytes", command, maxDescribeOutput)
	}
	if code != 0 {
		message := sanitize(string(stderr))
		message = strings.TrimSpace(tail(message))
		if message == "" {
			return nil, fmt.Errorf("discover tools from %s: %s", command, statusCodeString(code))
		}
		return nil, fmt.Errorf("discover tools from %s: %s", command, message)
	}
	var tools []Tool
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var spec externalToolSpec
		if err := json.Unmarshal([]byte(line), &spec); err != nil {
			return nil, fmt.Errorf("invalid tool from %s: %s", command, err)
		}
		if !validToolName(spec.Name) {
			return nil, fmt.Errorf("invalid tool name from %s: %s", command, spec.Name)
		}
		if strings.TrimSpace(spec.Description) == "" {
			return nil, fmt.Errorf("empty tool description from %s: %s", command, spec.Name)
		}
		schema, err := compileSchema(spec.Parameters)
		if err != nil {
			return nil, fmt.Errorf("invalid tool parameters from %s: %s", command, spec.Name+": "+err.Error())
		}
		for _, existing := range tools {
			if existing.Name == spec.Name {
				return nil, fmt.Errorf("duplicate tool name from %s: %s", command, spec.Name)
			}
		}
		executable := command
		toolName := spec.Name
		description := spec.Description
		parameters := append(json.RawMessage(nil), spec.Parameters...)
		snippet := spec.Snippet
		timeout := spec.Timeout
		tool := Tool{
			Name:        toolName,
			Description: description,
			Parameters:  parameters,
			Snippet:     snippet,
			Sequential:  spec.Sequential,
			Replay:      spec.Replay,
			ReadOnly:    spec.ReadOnly,
			Destructive: spec.Destructive,
			OpenWorld:   spec.OpenWorld,
			Timeout:     timeout,
			Validate:    schema.validate,
			RunContext: func(ctx context.Context, arguments string, _ func(string)) string {
				return runExternalTool(ctx, executable, toolName, arguments, workdir, timeout, env)
			},
		}
		if tool.Snippet == "" {
			tool.Snippet = tool.Description
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func runExternalTool(ctx context.Context, command, name, arguments, workdir string, timeoutSeconds int, env []string) string {
	ensureSignalHandler()
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultToolTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, command, "run", name)
	cmd.Dir = workdir
	if len(env) > 0 {
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(arguments)
	var stdout, stderr tailBuffer
	stdout.limit, stderr.limit = maxOutput, maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		go func() {
			time.Sleep(2 * time.Second)
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}()
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("error: %s", err)
	}
	registerGroup(cmd.Process.Pid)
	waitErr := cmd.Wait()
	unregisterGroup(cmd.Process.Pid)

	if runCtx.Err() == context.DeadlineExceeded || runCtx.Err() == context.Canceled {
		reason := "timed out"
		if errors.Is(runCtx.Err(), context.Canceled) {
			reason = "was cancelled"
		}
		message := fmt.Sprintf("error: %s %s after %ds; its side effects are unknown", command, reason, timeoutSeconds)
		if partial := strings.TrimSpace(sanitize(stdout.String())); partial != "" {
			message += "; partial output:\n" + tail(partial)
		}
		return message
	}
	if waitErr != nil {
		message := sanitize(stderr.String())
		if strings.TrimSpace(message) == "" {
			message = sanitize(stdout.String())
		}
		if strings.TrimSpace(message) == "" {
			return fmt.Sprintf("error: %s exited with %s", command, statusString(waitErr))
		}
		return tail(message)
	}
	result := sanitize(stdout.String())
	if strings.TrimSpace(result) == "" {
		return fmt.Sprintf("error: %s returned no output", command)
	}
	return tail(result)
}

func validateToolArguments(tool Tool, arguments string) error {
	if tool.Validate == nil {
		return nil
	}
	if err := tool.Validate(arguments); err != nil {
		return fmt.Errorf("invalid arguments for %s: %w", tool.Name, err)
	}
	return nil
}

func statusString(waitErr error) string {
	if waitErr == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return fmt.Sprintf("signal: %d", status.Signal())
			}
			return fmt.Sprintf("exit status: %d", status.ExitStatus())
		}
	}
	return waitErr.Error()
}

func statusCodeString(code int) string {
	return fmt.Sprintf("exit status: %d", code)
}
