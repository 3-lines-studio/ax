package main

import (
	"io"
	"os/exec"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// session tracks one ACP session/one ax --session file. acpi is a bridge,
// not an agent loop: all conversation state lives in ax's own JSONL session
// file, keyed by axSession. This struct only holds routing info plus the
// currently in-flight ax subprocess, if any, so Cancel can reach it.
type session struct {
	id         acp.SessionId
	cwd        string
	axSession  string
	mcpServers []acp.McpServer

	mu     sync.Mutex
	active *activeRun // non-nil only while a Prompt call is running ax
}

// activeRun is the running `ax --events` subprocess for a session's current
// turn. Its stdin stays open for the JSONL control channel ax documents:
// {"type":"cancel"} and {"type":"steer","text":"..."}.
type activeRun struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}
