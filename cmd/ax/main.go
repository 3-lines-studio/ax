package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/3-lines-studio/ax/internal/axproto"
)

type cliConfig struct {
	base        string
	model       string
	system      string
	dir         string
	session     string
	thinking    string
	effort      string
	maxTokens   int
	images      []string
	imageDetail string
}

type fileConfig struct {
	apiKey      string
	model       string
	base        string
	tools       string
	thinking    string
	effort      string
	maxTokens   int
	imageDetail string
}

func main() {
	args := os.Args[1:]
	fc := loadConfig()
	root := botRootArg(args)
	if root != "" {
		directory, err := filepath.Abs(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot use %s: %v\n", root, err)
			os.Exit(1)
		}
		canonical, err := filepath.EvalSymlinks(directory)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot use %s: %v\n", root, err)
			os.Exit(1)
		}
		if err := os.Chdir(canonical); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot use %s: %v\n", canonical, err)
			os.Exit(1)
		}
	}
	cfg, prompt, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		usage()
		os.Exit(2)
	}
	botRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: working directory")
		os.Exit(1)
	}
	if bc, err := loadBotConfig(botRoot); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	} else {
		fc = mergeBotConfig(fc, bc)
	}
	cfg.base = firstNonEmpty(cfg.base, fc.base, "https://api.deepseek.com")
	cfg.model = firstNonEmpty(cfg.model, fc.model, "deepseek-flash")
	bot := resolveBotCtx(botRoot)
	_ = os.Setenv("BOT_ROOT", bot.Root)
	if len(prompt) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: read stdin")
			os.Exit(1)
		}
		prompt = []string{string(data)}
	}
	oneShotEvents(cfg, fc, bot, prompt)
}

func parseArgs(args []string) (cliConfig, []string, error) {
	cfg := cliConfig{}
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
		case "base", "model", "system", "C", "session", "thinking", "effort", "max-tokens", "image", "image-detail":
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
			case "thinking":
				cfg.thinking = inline
			case "effort":
				cfg.effort = inline
			case "max-tokens":
				n, err := strconv.Atoi(inline)
				if err != nil || n < 0 {
					return cfg, nil, fmt.Errorf("invalid -max-tokens: %s", inline)
				}
				cfg.maxTokens = n
			case "image":
				cfg.images = append(cfg.images, inline)
			case "image-detail":
				cfg.imageDetail = inline
			}
		default:
			return cfg, nil, fmt.Errorf("flag provided but not defined: %s", a)
		}
		i++
	}
	return cfg, rest, nil
}

func usage() {
	fmt.Fprint(os.Stderr, "Usage: ax [flags] [prompt]\n\nFlags:\n  -base URL    OpenAI-compatible API base URL (default \"https://api.deepseek.com\")\n  -model NAME  model name (default \"deepseek-flash\")\n  -system TEXT  system prompt override (default: built-in + bot.md)\n  -C DIR       bot directory root (default: current directory)\n  --session FILE  append-only durable session log\n  --events      emit JSONL events on stdout\n  -thinking X  thinking mode: auto, on, off (default auto)\n  -effort X    reasoning effort: low, high, max (default max for DeepSeek)\n  -max-tokens N  maximum output tokens (default: provider default)\n  -image PATH|URL  attach an image to the prompt (repeatable)\n  -image-detail X  image detail: low, high, original, auto (default auto)\n\nWith no prompt, reads the prompt from stdin.\nA prompt of the form /name [args] expands a user command from\n~/.config/ax/commands/<name>.md.\n")
}

func oneShotEvents(cfg cliConfig, fc fileConfig, bot *botCtx, prompt []string) {
	if err := validateModelOptions(cfg, fc); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	printJSON(map[string]any{"type": axproto.TypeProtocol, "version": axproto.Version})

	var entries []sessionEntry
	if cfg.session != "" {
		var err error
		entries, err = loadPath(cfg.session)
		if err != nil {
			eventFailure(err.Error())
		}
	}
	state := deriveSession(entries)
	if state.OpenRun {
		fmt.Fprintf(os.Stderr, "warning: previous run did not finish; repaired %d interrupted tool call(s)\n", state.Repaired)
	}

	detail := firstNonEmpty(cfg.imageDetail, fc.imageDetail, "auto")
	images, err := loadImages(cfg.images, detail, cfg.session)
	if err != nil {
		eventFailure(err.Error())
	}

	tools := toolsOrExit(fc, bot.Workspace)
	system := resolveSystem(cfg, tools, bot)
	base := cfg.base
	model := cfg.model
	userMessage := buildUserMessage(expandPromptCommand(strings.Join(prompt, " "), axRoot()), images)
	history := append(append([]Message(nil), state.Messages...), userMessage)

	var log *sessionLog
	runID := newRunID()
	if cfg.session != "" {
		log, err = openSessionLog(cfg.session)
		if err != nil {
			eventFailure(err.Error())
		}
		if err := log.append(sessionEntry{
			Type: entryRunStart, RunID: runID, Prompt: userMessage.Text(),
			Model: model, BaseURL: base, Tools: toolNames(tools),
		}); err != nil {
			_ = log.close()
			eventFailure(err.Error())
		}
		if err := log.append(sessionEntry{Type: entryMessage, RunID: runID, Message: &userMessage}); err != nil {
			_ = log.close()
			eventFailure(err.Error())
		}
	}

	var cancel atomic.Bool
	if !stdinIsTerminal() {
		go readControls(&cancel)
	}

	provider := NewOpenAI(base, apiKey(fc, bot))
	provider.SessionPath = cfg.session
	provider.Thinking = thinkingOption(cfg, fc)
	provider.Effort = firstNonEmpty(cfg.effort, fc.effort)
	provider.MaxTokens = cfg.maxTokens
	if provider.MaxTokens == 0 {
		provider.MaxTokens = fc.maxTokens
	}

	sink := &eventSink{
		log: log, runID: runID, model: model,
		calls: map[string]ToolCall{},
	}
	end := runStream(provider, &runOptions{
		model:    model,
		system:   system,
		tools:    tools,
		maxTurns: int(^uint(0) >> 1),
	}, history, &cancel, sink)

	outcomeName := outcomeString(end.outcome)
	closeLog := func() {
		if log != nil {
			_ = log.close()
			log = nil
		}
	}
	if log != nil {
		if err := log.append(sessionEntry{
			Type: entryRunEnd, RunID: runID, Outcome: outcomeName, Usage: usageEntry(end.usage, model),
		}); err != nil {
			closeLog()
			eventFailure(err.Error())
		}
	}
	closeLog()

	finalUsage := end.usage
	printJSON(map[string]any{"type": axproto.TypeResult, "messages": end.messages, "usage": finalUsage})

	failed := false
	switch end.outcome {
	case outcomeCancelled:
		outcomeName = axproto.OutcomeCancelled
	case outcomeCompact:
		outcomeName = axproto.OutcomeCompact
	case outcomeMaxTurns:
		outcomeName, failed = axproto.OutcomeMaxTurns, true
	case outcomeFailed:
		outcomeName, failed = axproto.OutcomeFailed, true
		printJSON(map[string]any{"type": axproto.TypeError, "message": end.failMsg})
	default:
		outcomeName = axproto.OutcomeDone
	}
	printJSON(map[string]any{"type": axproto.TypeDone, "outcome": outcomeName})
	if failed {
		os.Exit(1)
	}
}

func outcomeString(out outcome) string {
	switch out {
	case outcomeCancelled:
		return axproto.OutcomeCancelled
	case outcomeCompact:
		return axproto.OutcomeCompact
	case outcomeMaxTurns:
		return axproto.OutcomeMaxTurns
	case outcomeFailed:
		return axproto.OutcomeFailed
	default:
		return axproto.OutcomeDone
	}
}

func usageEntry(usage Usage, model string) *usageInfo {
	return &usageInfo{
		Input: usage.Input, Output: usage.Output, CachedInput: usage.CachedInput,
		Reasoning: usage.Reasoning, ContextInput: usage.Input, ContextOutput: usage.Output,
		Model: model,
	}
}

func toolNames(tools []Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func newRunID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(buf[:]))
}

func thinkingOption(cfg cliConfig, fc fileConfig) *bool {
	if strings.EqualFold(firstNonEmpty(cfg.effort, fc.effort), "none") {
		value := false
		return &value
	}
	switch strings.ToLower(firstNonEmpty(cfg.thinking, fc.thinking, "auto")) {
	case "on", "true", "enabled":
		value := true
		return &value
	case "off", "false", "disabled":
		value := false
		return &value
	default:
		return nil
	}
}

func validateModelOptions(cfg cliConfig, fc fileConfig) error {
	switch strings.ToLower(firstNonEmpty(cfg.thinking, fc.thinking, "auto")) {
	case "auto", "on", "off", "true", "false", "enabled", "disabled":
	default:
		return fmt.Errorf("invalid -thinking: %s (use auto, on or off)", firstNonEmpty(cfg.thinking, fc.thinking))
	}
	switch strings.ToLower(firstNonEmpty(cfg.effort, fc.effort)) {
	case "", "low", "high", "max", "none":
	default:
		return fmt.Errorf("invalid -effort: %s (use low, high or max)", firstNonEmpty(cfg.effort, fc.effort))
	}
	switch strings.ToLower(firstNonEmpty(cfg.imageDetail, fc.imageDetail, "auto")) {
	case "low", "high", "original", "auto":
	default:
		return fmt.Errorf("invalid -image-detail: %s (use low, high, original or auto)", firstNonEmpty(cfg.imageDetail, fc.imageDetail))
	}
	maxTokens := cfg.maxTokens
	if maxTokens == 0 {
		maxTokens = fc.maxTokens
	}
	if maxTokens > 393216 {
		return fmt.Errorf("-max-tokens %d exceeds DeepSeek's 393216 limit", maxTokens)
	}
	return nil
}

func eventFailure(message string) {
	printJSON(map[string]any{"type": axproto.TypeError, "message": message})
	printJSON(map[string]any{"type": axproto.TypeDone, "outcome": axproto.OutcomeFailed})
	os.Exit(1)
}

func printJSON(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: encode event: %v\n", err)
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

func toolsOrExit(config fileConfig, workdir string) []Tool {
	commands := config.tools
	if value, present := os.LookupEnv("AX_TOOLS"); present {
		commands = value
	}
	var tools []Tool
	if commands != "" {
		var err error
		tools, err = tryExternalTools(commands, workdir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
	return tools
}

func apiKey(fc fileConfig, bot *botCtx) string {
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return key
	}
	if bot != nil && bot.HasAPIKey {
		return bot.APIKey
	}
	return fc.apiKey
}

func resolveSystem(cfg cliConfig, tools []Tool, bot *botCtx) string {
	if cfg.system != "" {
		return cfg.system
	}
	return buildSystemPrompt(tools, bot.Workspace, bot)
}

func buildSystemPrompt(tools []Tool, dir string, bot *botCtx) string {
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
	if bot != nil && bot.Prompt != "" {
		out.WriteString("\n\n" + bot.Prompt)
	}
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
		case "thinking":
			c.thinking = val
		case "effort", "reasoning_effort":
			c.effort = val
		case "max_tokens":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				c.maxTokens = n
			}
		case "image_detail":
			c.imageDetail = val
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

func readControls(cancel *atomic.Bool) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var control struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &control); err != nil {
			continue
		}
		if control.Type == "cancel" {
			cancel.Store(true)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type eventSink struct {
	log              *sessionLog
	runID            string
	model            string
	turn             int
	calls            map[string]ToolCall
	partialContent   strings.Builder
	partialReasoning strings.Builder
	partialLen       int
	partialAt        time.Time
	lastUsage        *Usage
}

func (s *eventSink) emit(value any) { printJSON(value) }

func (s *eventSink) append(entry sessionEntry) {
	if s.log == nil {
		return
	}
	entry.RunID = s.runID
	if err := s.log.append(entry); err != nil {
		eventFailure(err.Error())
	}
}

// maybeFlushPartial writes the in-progress assistant turn at most once per
// second or per 4 KiB. A crash keeps the partial for audit; recovery never
// sends an incomplete assistant turn to the model.
func (s *eventSink) maybeFlushPartial() {
	if s.log == nil {
		return
	}
	total := s.partialContent.Len() + s.partialReasoning.Len()
	if total == s.partialLen {
		return
	}
	if s.partialLen == 0 {
		s.partialAt = time.Now()
	}
	if total-s.partialLen < 4096 && time.Since(s.partialAt) < time.Second {
		return
	}
	message := Message{
		Role:             "assistant",
		Content:          s.partialContent.String(),
		ReasoningContent: s.partialReasoning.String(),
	}
	s.append(sessionEntry{Type: entryAssistantPartial, Turn: s.turn, Message: &message})
	s.partialLen = total
	s.partialAt = time.Now()
}

func (s *eventSink) assistantDelta(text string) {
	s.partialContent.WriteString(text)
	s.emit(map[string]any{"type": axproto.TypeAssistantDelta, "text": text})
	s.maybeFlushPartial()
}

func (s *eventSink) reasoningDelta(text string) {
	s.partialReasoning.WriteString(text)
	s.emit(map[string]any{"type": axproto.TypeReasoningDelta, "text": text})
	s.maybeFlushPartial()
}

func (s *eventSink) assistantDone() {
	s.emit(map[string]any{"type": axproto.TypeAssistantDone})
}

func (s *eventSink) toolStart(call ToolCall) {
	if s.calls == nil {
		s.calls = map[string]ToolCall{}
	}
	s.calls[call.ID] = call
	s.append(sessionEntry{Type: entryToolIntent, Turn: s.turn, Call: &call})
	s.emit(map[string]any{"type": axproto.TypeToolStart, "id": call.ID, "name": call.Name, "arguments": call.Arguments})
}

func (s *eventSink) toolDelta(call ToolCall, text string) {
	s.emit(map[string]any{"type": axproto.TypeToolDelta, "id": call.ID, "text": text})
}

func (s *eventSink) toolResult(call ToolCall) {
	s.emit(map[string]any{"type": axproto.TypeToolDone, "id": call.ID})
}

func (s *eventSink) tokens(input, output, cached int) {
	u := Usage{Input: input, Output: output, CachedInput: cached}
	s.lastUsage = &u
	s.emit(map[string]any{"type": axproto.TypeUsage, "input": input, "output": output, "cached_input": cached, "model": s.model})
}

func (s *eventSink) shouldCompact(input, output int) bool { return false }

func (s *eventSink) assistant(turn int, msg Message, usage Usage) {
	s.turn = turn
	message := msg
	s.append(sessionEntry{Type: entryMessage, Turn: turn, Message: &message})
	s.append(sessionEntry{Type: entryUsage, Turn: turn, Usage: usageEntry(usage, s.model)})
	s.partialContent.Reset()
	s.partialReasoning.Reset()
	s.partialLen = 0
	s.emit(map[string]any{"type": axproto.TypeMessage, "message": msg})
}

func (s *eventSink) tool(turn int, msg Message) {
	call, ok := s.calls[msg.ToolCallID]
	if !ok {
		call = ToolCall{ID: msg.ToolCallID}
	}
	result := msg
	s.append(sessionEntry{Type: entryToolResult, Turn: turn, Call: &call, Output: result.Content})
	s.emit(map[string]any{"type": axproto.TypeToolResult, "id": msg.ToolCallID, "output": msg.Content})
	s.emit(map[string]any{"type": axproto.TypeMessage, "message": msg})
}
