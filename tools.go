package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

const maxOutput = 16 * 1024
const maxDescribeOutput = 64 * 1024

type externalToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Snippet     string          `json:"snippet"`
}

var (
	childGroups sync.Map
	signalOnce  sync.Once
)

// ensureSignalHandler reaps child process groups when ax is itself
// terminated, so a killed run never orphans its tool children.
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
		} else if code > 0x1f && !(code >= 0xfff9 && code <= 0xfffb) {
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

// boundedBuffer keeps output up to limit and records overflow without
// blocking the child (see Rust bounded_output semantics).
type boundedBuffer struct {
	buf      []byte
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
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

// boundedOutput runs a discovery command, capturing stdout/stderr bounded at
// limit. Returns the exit code (or -1 on start error), the outputs, and any
// start error.
func boundedOutput(command *exec.Cmd, limit int) (int, []byte, []byte, error) {
	ensureSignalHandler()
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = limit, limit
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return -1, nil, nil, err
	}
	registerGroup(command.Process.Pid)
	waitErr := command.Wait()
	unregisterGroup(command.Process.Pid)
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), stdout.buf, stderr.buf, nil
		}
		return -1, nil, nil, waitErr
	}
	return 0, stdout.buf, stderr.buf, nil
}

func tryExternalTools(commands string, workdir string) ([]Tool, error) {
	var tools []Tool
	for _, command := range strings.Fields(commands) {
		describe := exec.Command(command, "describe")
		describe.Dir = workdir
		code, stdout, stderr, err := boundedOutput(describe, maxDescribeOutput)
		if err != nil {
			return nil, fmt.Errorf("discover tools from %s: %w", command, err)
		}
		if len(stdout) > maxDescribeOutput {
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
			if !isJSONObject(spec.Parameters) {
				return nil, fmt.Errorf("invalid tool parameters from %s: %s", command, spec.Name)
			}
			for _, existing := range tools {
				if existing.Name == spec.Name {
					return nil, fmt.Errorf("duplicate tool name: %s", spec.Name)
				}
			}
			executable := command
			toolName := spec.Name
			description := spec.Description
			parameters := append(json.RawMessage(nil), spec.Parameters...)
			snippet := spec.Snippet
			tool := Tool{
				Name:        toolName,
				Description: description,
				Parameters:  parameters,
				Snippet:     snippet,
				Sequential:  false,
				Run: func(arguments string, _ func(string)) string {
					return runExternalTool(executable, toolName, arguments, workdir)
				},
			}
			if tool.Snippet == "" {
				tool.Snippet = tool.Description
			}
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

func isJSONObject(raw json.RawMessage) bool {
	var obj map[string]interface{}
	return json.Unmarshal(raw, &obj) == nil
}

func runExternalTool(command, name, arguments, workdir string) string {
	ensureSignalHandler()
	cmd := exec.Command(command, "run", name)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(arguments)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("error: %s", err)
	}
	registerGroup(cmd.Process.Pid)
	waitErr := cmd.Wait()
	unregisterGroup(cmd.Process.Pid)
	if waitErr != nil {
		message := sanitize(stderr.String())
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
