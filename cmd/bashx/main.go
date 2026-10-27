package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/3-lines-studio/ax/internal/botdir"
)

const describe = `{"name":"bash","description":"Execute non-interactive Bash with host filesystem and network access. Inherits the host environment. Returns stdout and stderr. Commands time out after 120 seconds by default.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"Bash command to run"},"timeout":{"type":"number","minimum":1,"maximum":600,"description":"Timeout in seconds (default 120, maximum 600)"}},"required":["command"],"additionalProperties":false},"sequential":true,"snippet":"Execute Bash commands"}`

const (
	maxInput    = 1 << 20
	maxOutput   = 16 * 1024
	defaultTime = 120
	maxTimeout  = 600
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	switch {
	case len(args) == 1 && args[0] == "describe":
		fmt.Println(describe)
	case len(args) == 2 && args[0] == "run" && args[1] == "bash":
		if code, message := run(); code != 0 {
			fail(code, message)
		}
	case len(args) == 2 && args[0] == "run":
		fail(2, "unknown tool")
	default:
		fail(2, "usage: bashx describe | bashx run bash")
	}
}

func fail(code int, message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(code)
}

func run() (int, string) {
	input, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput+1))
	if err != nil {
		return 1, err.Error()
	}
	if len(input) > maxInput {
		return 2, "input exceeds 1 MiB"
	}
	var arguments struct {
		Command string `json:"command"`
		Timeout *int64 `json:"timeout"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return 2, "invalid input: " + err.Error()
	}
	if arguments.Command == "" {
		return 2, "command is required"
	}
	timeout := int64(defaultTime)
	if arguments.Timeout != nil {
		if *arguments.Timeout < 1 || *arguments.Timeout > maxTimeout {
			return 2, fmt.Sprintf("timeout must be between 1 and %d", maxTimeout)
		}
		timeout = *arguments.Timeout
	}
	workspace, err := workspacePath()
	if err != nil {
		return 1, err.Error()
	}

	command := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", arguments.Command)
	command.Dir = workspace
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr cappedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Start(); err != nil {
		return 1, err.Error()
	}
	pgid := command.Process.Pid

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()

	timeoutTimer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timeoutTimer.Stop()

	var timedOut bool
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-timeoutTimer.C:
		killGroup(pgid)
		timedOut = true
		waitErr = <-waitDone
	case <-signals:
		killGroup(pgid)
		waitErr = <-waitDone
	}

	output := stdout.String()
	if stderrText := stderr.String(); stderrText != "" {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += stderrText
	}
	switch {
	case timedOut:
		output = appendLine(output, fmt.Sprintf("error: command timed out after %d seconds", timeout))
	case waitErr != nil:
		output = appendLine(output, "error: "+statusString(waitErr))
	}
	if output == "" {
		output = "Command completed without output"
	}
	output = strings.ToValidUTF8(output, "\uFFFD")
	fmt.Print(tail(output))
	return 0, ""
}

func workspacePath() (string, error) {
	absolute, err := filepath.Abs(botdir.Workspace())
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %s is not a directory", absolute)
	}
	return absolute, nil
}

func killGroup(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
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

func appendLine(output, line string) string {
	if output != "" && !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return output + line
}

func tail(output string) string {
	if len(output) <= maxOutput {
		return output
	}
	start := len(output) - maxOutput
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	return output[start:]
}

type cappedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > maxOutput {
		b.buf = b.buf[len(b.buf)-maxOutput:]
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
