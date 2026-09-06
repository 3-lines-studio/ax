package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

type cliConfig struct {
	base    string
	model   string
	system  string
	dir     string
	session string
}

type fileConfig struct {
	apiKey string
	model  string
	base   string
	tools  string
}

func main() {
	args := os.Args[1:]
	fc := loadConfig()
	cfg, prompt, err := parseArgs(args, fc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		usage()
		os.Exit(2)
	}
	if cfg.dir != "" {
		directory, err := filepath.Abs(cfg.dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot use %s: %v\n", cfg.dir, err)
			os.Exit(1)
		}
		canonical, err := filepath.EvalSymlinks(directory)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot use %s: %v\n", cfg.dir, err)
			os.Exit(1)
		}
		if err := os.Chdir(canonical); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot use %s: %v\n", canonical, err)
			os.Exit(1)
		}
		cfg.dir = canonical
	}
	if len(prompt) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: read stdin")
			os.Exit(1)
		}
		prompt = []string{string(data)}
	}
	oneShotEvents(cfg, fc, prompt)
}

func parseArgs(args []string, fc fileConfig) (cliConfig, []string, error) {
	cfg := cliConfig{
		base:  firstNonEmpty(fc.base, "https://api.openai.com/v1"),
		model: firstNonEmpty(fc.model, "gpt-4.1-mini"),
	}
	var rest []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-h" || a == "--help" {
			usage()
			os.Exit(0)
		}
		if a == "--events" {
			i++
			continue
		}
		if a == "--" {
			rest = args[i+1:]
			break
		}
		if a == "" || !strings.HasPrefix(a, "-") {
			rest = args[i:]
			break
		}
		stripped := strings.TrimPrefix(a, "-")
		stripped = strings.TrimPrefix(stripped, "-")
		name, inline, hasInline := stripped, "", false
		if idx := strings.IndexByte(stripped, '='); idx >= 0 {
			name, inline, hasInline = stripped[:idx], stripped[idx+1:], true
		}
		switch name {
		case "base", "model", "system", "C", "session":
			if !hasInline {
				i++
				if i >= len(args) {
					return cfg, nil, fmt.Errorf("flag needs an argument: %s", a)
				}
				inline = args[i]
			}
			switch name {
			case "base":
				cfg.base = inline
			case "model":
				cfg.model = inline
			case "system":
				cfg.system = inline
			case "C":
				cfg.dir = inline
			case "session":
				cfg.session = inline
			}
		default:
			return cfg, nil, fmt.Errorf("flag provided but not defined: %s", a)
		}
		i++
	}
	return cfg, rest, nil
}

func usage() {
	fmt.Fprint(os.Stderr, "Usage: ax [flags] [prompt]\n\nFlags:\n  -base URL    OpenAI-compatible API base URL (default \"https://api.openai.com/v1\")\n  -model NAME  model name (default \"gpt-4.1-mini\")\n  -system TEXT  system prompt (default: built-in)\n  -C DIR       working directory context\n  --session FILE  use an explicit session file\n  --events      emit JSONL events on stdout\n\nWith no prompt, reads the prompt from stdin.\nA prompt of the form /name [args] expands a user command from\n~/.config/ax/commands/<name>.md.\n")
}

func oneShotEvents(cfg cliConfig, fc fileConfig, prompt []string) {
	printJSON(map[string]any{"type": "protocol", "version": 1})
	var history []Message
	if cfg.session != "" {
		entries, err := loadPath(cfg.session)
		if err != nil {
			eventFailure(err.Error())
		}
		history = contextMessages(entries)
	}
	history = append(history, Message{Role: "user", Content: expandPromptCommand(strings.Join(prompt, " "), axRoot())})
	oldLen := len(history) - 1

	var cancel atomic.Bool
	steer := make(chan string, 8)
	if !stdinIsTerminal() {
		go readControls(&cancel, steer)
	}

	tools := toolsOrExit(fc)
	system := resolveSystem(cfg, tools)
	provider := NewOpenAI(cfg.base, apiKey(fc))
	sink := &eventSink{
		steer:     steer,
		session:   cfg.session,
		seen:      []Message{},
		flushed:   0,
		model:     cfg.model,
		hostTools: hostToolNames(),
	}
	sink.record(history[oldLen])
	end := runStream(provider, &runOptions{
		model:    cfg.model,
		system:   system,
		tools:    tools,
		maxTurns: int(^uint(0) >> 1),
	}, history, &cancel, sink)
	sink.finalize(end.messages, oldLen)

	finalUsage := end.usage
	if sink.lastUsage != nil {
		finalUsage = *sink.lastUsage
	}
	if cfg.session != "" {
		if err := appendUsage(cfg.session, finalUsage.Input, finalUsage.Output, finalUsage.CachedInput, nil, cfg.model); err != nil {
			eventFailure(err.Error())
		}
	}
	printJSON(map[string]any{"type": "result", "messages": end.messages, "usage": finalUsage})

	outcomeName := ""
	failed := false
	switch end.outcome {
	case outcomeCancelled:
		outcomeName = "cancelled"
	case outcomeCompact:
		outcomeName = "compact"
	case outcomeMaxTurns:
		outcomeName, failed = "max_turns", true
	case outcomeFailed:
		outcomeName, failed = "failed", true
		printJSON(map[string]any{"type": "error", "message": end.failMsg})
	default:
		outcomeName = "done"
	}
	printJSON(map[string]any{"type": "done", "outcome": outcomeName})
	if failed {
		os.Exit(1)
	}
}

func eventFailure(message string) {
	printJSON(map[string]any{"type": "error", "message": message})
	printJSON(map[string]any{"type": "done", "outcome": "failed"})
	os.Exit(1)
}

func printJSON(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	fmt.Println(string(data))
}

func expandPromptCommand(prompt, axRoot string) string {
	cmd, ok := strings.CutPrefix(prompt, "/")
	if !ok {
		return prompt
	}
	name, rest := cmd, ""
	if idx := strings.IndexByte(cmd, ' '); idx >= 0 {
		name, rest = cmd[:idx], strings.TrimSpace(cmd[idx+1:])
	}
	for _, uc := range loadUserCommands(axRoot) {
		if uc.name == name {
			return expandUserCommand(uc, rest)
		}
	}
	return prompt
}

func toolsOrExit(config fileConfig) []Tool {
	commands := config.tools
	if value, present := os.LookupEnv("AX_TOOLS"); present {
		commands = value
	}
	var tools []Tool
	if commands != "" {
		var err error
		tools, err = tryExternalTools(commands)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
	return append(tools, hostToolsOrExit()...)
}

func hostToolsOrExit() []Tool {
	value := os.Getenv("AX_HOST_TOOLS")
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var definitions []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(value), &definitions); err != nil {
		fmt.Fprintln(os.Stderr, "error: invalid AX_HOST_TOOLS:", err)
		os.Exit(1)
	}
	var tools []Tool
	for _, def := range definitions {
		name := def.Name
		description := def.Description
		params := append(json.RawMessage(nil), def.Parameters...)
		tools = append(tools, Tool{
			Name:        name,
			Description: description,
			Parameters:  params,
			Snippet:     "",
			Sequential:  true,
			Run: func(_ string, _ func(string)) string {
				return "Host accepted the operation."
			},
		})
	}
	return tools
}

func hostToolNames() map[string]bool {
	out := map[string]bool{}
	value := os.Getenv("AX_HOST_TOOLS")
	if strings.TrimSpace(value) == "" {
		return out
	}
	var definitions []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(value), &definitions); err != nil {
		return out
	}
	for _, def := range definitions {
		if def.Name != "" {
			out[def.Name] = true
		}
	}
	return out
}

func apiKey(fc fileConfig) string {
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return key
	}
	return fc.apiKey
}

func workDir(cfg cliConfig) string {
	if cfg.dir != "" {
		return cfg.dir
	}
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return ""
}

func resolveSystem(cfg cliConfig, tools []Tool) string {
	if cfg.system != "" {
		return cfg.system
	}
	return buildSystemPrompt(tools, workDir(cfg))
}

func buildSystemPrompt(tools []Tool, dir string) string {
	var out strings.Builder
	out.WriteString("You are an expert assistant operating inside ax.\n\nAvailable tools:\n")
	for _, tool := range tools {
		if tool.Snippet != "" {
			fmt.Fprintf(&out, "- %s: %s\n", tool.Name, tool.Snippet)
		}
	}
	if len(tools) == 0 {
		out.WriteString("- none\n")
	}
	out.WriteString("\nGuidelines:\n- Be concise in your responses\n- Tool errors return to you as text; fix them and re-issue\n")
	for _, tool := range tools {
		if tool.Name == "edit" {
			out.WriteString("- Show file paths clearly when working with files\n- Use edit for precise changes; edits[].oldText must match exactly\n- When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls\n- Keep edits[].oldText small while still unique; do not pad with unchanged regions\n")
		}
	}
	fmt.Fprintf(&out, "\nCurrent working directory: %s", dir)
	if up := userSystemPrompt(); up != "" {
		out.WriteString("\n\n" + up)
	}
	return out.String()
}

func userSystemPrompt() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "ax", "SYSTEM.md"))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ""
	}
	return text
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config")
	}
	return ""
}

func axRoot() string {
	if dir := configDir(); dir != "" {
		return filepath.Join(dir, "ax")
	}
	return workDirAbs()
}

func workDirAbs() string {
	if dir, err := os.Getwd(); err == nil {
		return filepath.Join(dir, ".ax")
	}
	return ".ax"
}

func loadConfig() fileConfig {
	c := fileConfig{}
	dir := configDir()
	if dir == "" {
		return c
	}
	data, err := os.ReadFile(filepath.Join(dir, "ax", "config"))
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		val := strings.TrimSpace(value)
		if len(val) >= 2 && strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"") {
			val = val[1 : len(val)-1]
		}
		switch strings.TrimSpace(key) {
		case "api_key":
			c.apiKey = val
		case "model":
			c.model = val
		case "base":
			c.base = val
		case "tools":
			c.tools = val
		}
	}
	return c
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func readControls(cancel *atomic.Bool, steer chan string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var control struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &control); err != nil {
			continue
		}
		switch control.Type {
		case "cancel":
			cancel.Store(true)
		case "steer":
			if control.Text != "" {
				select {
				case steer <- control.Text:
				default:
				}
			}
		}
	}
}

func firstNonEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// --- eventSink -----------------------------------------------------------

type eventSink struct {
	steer        chan string
	session      string
	seen         []Message
	flushed      int
	pendingCalls *int
	results      int
	lastUsage    *Usage
	model        string
	hostTools    map[string]bool
}

func (s *eventSink) emit(value any) { printJSON(value) }

func (s *eventSink) record(message Message) {
	holding := s.pendingCalls != nil
	s.seen = append(s.seen, message)
	last := s.seen[len(s.seen)-1]
	if last.Role == "assistant" && len(last.ToolCalls) > 0 {
		n := len(last.ToolCalls)
		s.pendingCalls = &n
		s.results = 0
		return
	}
	if holding {
		if last.Role == "tool" {
			s.results++
			if s.results >= *s.pendingCalls {
				s.pendingCalls = nil
				s.flush()
			}
		}
		return
	}
	s.flush()
}

func (s *eventSink) flush() {
	if s.session == "" {
		return
	}
	if s.flushed >= len(s.seen) {
		return
	}
	if err := appendMessages(s.session, s.seen[s.flushed:]); err != nil {
		eventFailure(err.Error())
	}
	s.flushed = len(s.seen)
}

func (s *eventSink) finalize(messages []Message, base int) {
	if s.session == "" {
		return
	}
	held := s.seen[s.flushed:]
	var rest []Message
	if base+len(s.seen) < len(messages) {
		rest = messages[base+len(s.seen):]
	}
	if len(held) == 0 && len(rest) == 0 {
		return
	}
	tail := append([]Message(nil), held...)
	tail = append(tail, rest...)
	if err := appendMessages(s.session, tail); err != nil {
		eventFailure(err.Error())
	}
	s.flushed = len(s.seen)
}

func (s *eventSink) assistantDelta(text string) {
	s.emit(map[string]any{"type": "assistant_delta", "text": text})
}

func (s *eventSink) assistantDone() {
	s.emit(map[string]any{"type": "assistant_done"})
}

func (s *eventSink) toolStart(call ToolCall) {
	s.emit(map[string]any{"type": "tool_start", "id": call.ID, "name": call.Name, "arguments": call.Arguments})
	if s.hostTools[call.Name] {
		s.emit(map[string]any{"type": "host_call", "id": call.ID, "name": call.Name, "arguments": call.Arguments})
	}
}

func (s *eventSink) toolDelta(call ToolCall, text string) {
	s.emit(map[string]any{"type": "tool_delta", "id": call.ID, "text": text})
}

func (s *eventSink) toolResult(call ToolCall) {
	s.emit(map[string]any{"type": "tool_done", "id": call.ID})
}

func (s *eventSink) tokens(input, output, cached int) {
	u := Usage{Input: input, Output: output, CachedInput: cached}
	s.lastUsage = &u
	s.emit(map[string]any{"type": "usage", "input": input, "output": output, "cached_input": cached, "model": s.model})
}

func (s *eventSink) shouldCompact(input, output int) bool { return false }

func (s *eventSink) assistant(turn int, msg Message, usage Usage) {
	s.emit(map[string]any{"type": "message", "message": msg})
	s.record(msg)
}

func (s *eventSink) tool(turn int, msg Message) {
	s.emit(map[string]any{"type": "tool_result", "id": msg.ToolCallID, "output": msg.Content})
	s.emit(map[string]any{"type": "message", "message": msg})
	s.record(msg)
}

func (s *eventSink) pendingUserInput() (string, bool) {
	select {
	case text := <-s.steer:
		s.record(Message{Role: "user", Content: text})
		return text, true
	default:
		return "", false
	}
}
