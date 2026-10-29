package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxInput = 8 * 1024 * 1024
const maxOutput = 16 * 1024

const (
	defaultTimeout = 30
	maxTimeout     = 300
)

const describeRead = `{"name":"read","description":"Read the contents of a file on the remote host. Output is truncated to 16KB. Use offset/limit for large files. When you need the full file, continue with the suggested offset.","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the remote root (or absolute inside it)"},"offset":{"type":"integer","minimum":1,"description":"Line number to start reading from (1-indexed)"},"limit":{"type":"integer","minimum":0,"description":"Maximum number of lines to read"}},"required":["path"],"additionalProperties":false},"read_only":true,"snippet":"Read a file on the remote host"}`

const describeWrite = `{"name":"write","description":"Write content to a file on the remote host. Creates the file if it does not exist, overwrites it if it does, and creates parent directories. The write is atomic: readers see the old file or the new one, never a partial one.","parameters":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false},"sequential":true,"destructive":true,"snippet":"Write a file on the remote host"}`

const describeEdit = `{"name":"edit","description":"Edit a file on the remote host using exact text replacements. Read the file first. Every oldText must match one unique, non-overlapping region.","parameters":{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","minItems":1,"items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["oldText","newText"],"additionalProperties":false}}},"required":["path","edits"],"additionalProperties":false},"sequential":true,"destructive":true,"snippet":"Edit a file on the remote host"}`

const describeBash = `{"name":"bash","description":"Execute non-interactive Bash on the remote host. Returns stdout and stderr. Commands time out after 120 seconds by default.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"Bash command to run"},"timeout":{"type":"number","minimum":1,"maximum":600,"description":"Timeout in seconds (default 120, maximum 600)"}},"required":["command"],"additionalProperties":false},"sequential":true,"destructive":true,"open_world":true,"snippet":"Execute Bash on the remote host"}`

var descriptors = []string{describeRead, describeWrite, describeEdit, describeBash}

type readArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type edit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type editArgs struct {
	Path  string `json:"path"`
	Edits []edit `json:"edits"`
}

type bashArgs struct {
	Command string `json:"command"`
	Timeout *int   `json:"timeout"`
}

type config struct {
	host       string
	root       string
	keyFile    string
	knownHosts string
	timeout    time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		host:       strings.TrimSpace(os.Getenv("SSHX_HOST")),
		root:       strings.TrimRight(strings.TrimSpace(os.Getenv("SSHX_ROOT")), "/"),
		keyFile:    strings.TrimSpace(os.Getenv("SSHX_KEY_FILE")),
		knownHosts: strings.TrimSpace(os.Getenv("SSHX_KNOWN_HOSTS")),
		timeout:    defaultTimeout * time.Second,
	}
	if cfg.host == "" {
		return config{}, errors.New("SSHX_HOST is required")
	}
	if cfg.knownHosts == "" {
		return config{}, errors.New("SSHX_KNOWN_HOSTS is required: a connection that accepts any host key is a MITM with a shell")
	}
	if value := strings.TrimSpace(os.Getenv("SSHX_TIMEOUT")); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > maxTimeout {
			return config{}, fmt.Errorf("SSHX_TIMEOUT must be 1-%d seconds", maxTimeout)
		}
		cfg.timeout = time.Duration(seconds) * time.Second
	}
	return cfg, nil
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 1 && args[0] == "describe" {
		for _, descriptor := range descriptors {
			fmt.Println(descriptor)
		}
		return
	}
	if len(args) == 2 && args[0] == "run" {
		run(args[1])
		return
	}
	fmt.Fprintln(os.Stderr, "usage: sshx describe | sshx run <tool>")
	os.Exit(2)
}

func run(tool string) {
	switch tool {
	case "read", "write", "edit", "bash":
	default:
		fmt.Fprintf(os.Stderr, "sshx: unknown tool %s\n", tool)
		os.Exit(2)
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput))
	if err == nil && !json.Valid(input) {
		fmt.Fprintln(os.Stderr, "sshx: invalid arguments: expected one JSON object")
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		fail(err)
	}
	remote, err := dial(cfg)
	if err != nil {
		fail(err)
	}
	defer func() { _ = remote.Close() }()

	switch tool {
	case "read":
		var args readArgs
		decode(input, &args)
		out, err := remote.read(args)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	case "write":
		var args writeArgs
		decode(input, &args)
		out, err := remote.write(args)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	case "edit":
		var args editArgs
		decode(input, &args)
		out, err := remote.edit(args)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	case "bash":
		var args bashArgs
		decode(input, &args)
		out, err := remote.bash(args)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	}
}

func decode(input []byte, value any) {
	if err := json.Unmarshal(input, value); err != nil {
		fmt.Fprintf(os.Stderr, "sshx: invalid arguments: %s\n", err)
		os.Exit(2)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "sshx: %s\n", err)
	os.Exit(1)
}
