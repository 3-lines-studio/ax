package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/3-lines-studio/ax/internal/axproto"
	"github.com/3-lines-studio/ax/internal/catalog"
)

const catalogLoadTimeout = 20 * time.Second

type cliConfig struct {
	base             string
	model            string
	system           string
	dir              string
	session          string
	thinking         string
	effort           string
	maxTokens        int
	images           []string
	imageDetail      string
	toolMode         string
	compactAt        float64
	compactAtSet     bool
	contextWindow    int
	contextWindowSet bool
	resume           bool
	literal          bool
}

type fileConfig struct {
	apiKey           string
	model            string
	base             string
	tools            string
	thinking         string
	effort           string
	maxTokens        int
	imageDetail      string
	toolMode         string
	coreTools        []string
	mcp              []mcpEntry
	connections      []connectionEntry
	computeIDs       []string
	computeErr       error
	compactAt        float64
	compactAtSet     bool
	contextWindow    int
	contextWindowSet bool
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
	if cfg.toolMode == "" {
		cfg.toolMode = firstNonEmpty(fc.toolMode, "catalog")
	}
	bot := resolveBotCtx(botRoot)
	_ = os.Setenv("BOT_ROOT", bot.Root)
	// A broken compute root must not hide `ax conn list`/`ax conn doctor`: the
	// run path fails on it, the diagnostics report it.
	compute, computeErr := resolveCompute(fc, bot.Root)
	fc.computeIDs = compute.ids
	fc.computeErr = computeErr
	if compute.root != "" {
		bot.Workspace = compute.root
	}
	promptFromStdin := len(prompt) == 0 && !cfg.resume
	if promptFromStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: read stdin")
			os.Exit(1)
		}
		prompt = []string{string(data)}
	}
	if len(prompt) > 0 && !cfg.literal && !promptFromStdin {
		switch prompt[0] {
		case "catalog":
			runCatalogCommand(fc, bot, prompt[1:])
			return
		case "compact":
			runCompactCommand(cfg, fc, bot, prompt[1:])
			return
		case "conn":
			runConnCommand(fc, bot, prompt[1:])
			return
		case "selftest":
			runSelftest(cfg, fc, bot)
			return
		case "mcp":
			runMCPCommand(fc, bot, prompt[1:])
			return
		case "routine":
			runRoutineCommand(bot, prompt[1:])
			return
		}
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
		if a == "--resume" || a == "-resume" {
			cfg.resume = true
			i++
			continue
		}
		if a == "--" {
			cfg.literal = true
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
		case "base", "model", "system", "C", "session", "thinking", "effort", "max-tokens", "image", "image-detail", "tool-mode", "compact-at", "context-window":
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
			case "tool-mode":
				cfg.toolMode = inline
			case "compact-at":
				value, err := strconv.ParseFloat(inline, 64)
				if err != nil || value < 0 || value >= 1 {
					return cfg, nil, fmt.Errorf("invalid -compact-at: %s (use 0 to disable, or a fraction between 0 and 1)", inline)
				}
				cfg.compactAt, cfg.compactAtSet = value, true
			case "context-window":
				value, err := strconv.Atoi(inline)
				if err != nil || value < 0 {
					return cfg, nil, fmt.Errorf("invalid -context-window: %s", inline)
				}
				cfg.contextWindow, cfg.contextWindowSet = value, true
			}
		default:
			return cfg, nil, fmt.Errorf("flag provided but not defined: %s", a)
		}
		i++
	}
	return cfg, rest, nil
}

func usage() {
	fmt.Fprint(os.Stderr, "Usage: ax [flags] [prompt]\n\nFlags:\n  -base URL    OpenAI-compatible API base URL (default \"https://api.deepseek.com\")\n  -model NAME  model name (default \"deepseek-flash\")\n  -system TEXT  system prompt override (default: built-in + bot.md)\n  -C DIR       bot directory root (default: current directory)\n  --session FILE  append-only durable session log\n  --events      emit JSONL events on stdout\n  -thinking X  thinking mode: auto, on, off (default auto)\n  -effort X    reasoning effort: low, high, max (default max for DeepSeek)\n  -max-tokens N  maximum output tokens (default: provider default)\n  -image PATH|URL  attach an image to the prompt (repeatable)\n  -image-detail X  image detail: low, high, original, auto (default auto)\n  -tool-mode X  tool surface: catalog or direct (default catalog)\n  -compact-at X  compact when the prompt reaches this fraction of the window\n  -context-window N  context window in tokens (default 1000000)\n\nCommands:\n  ax catalog [--json] [--refresh]   print the tool catalog\n  ax compact --session FILE         summarize a session into a shorter prompt\n  ax mcp serve [--stdio | --http ADDR]  expose the catalog to MCP hosts\n  ax mcp add|import|list            connect servers (registry, mcp.json)\n  ax mcp login|logout|doctor        OAuth sessions and diagnostics\n  ax selftest                       check config, connections and catalog, no model call\n\nWith no prompt, reads the prompt from stdin.\nA prompt of the form /name [args] expands a user command from\n~/.config/ax/commands/<name>.md.\n")
}

func oneShotEvents(cfg cliConfig, fc fileConfig, bot *botCtx, prompt []string) {
	if fc.computeErr != nil {
		fmt.Fprintln(os.Stderr, "error:", fc.computeErr)
		os.Exit(1)
	}
	if err := validateModelOptions(cfg, fc); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := validateToolMode(cfg, fc); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if compactAt, window := resolveCompact(cfg, fc); compactAt >= 1 || compactAt < 0 || window < 0 {
		fmt.Fprintf(os.Stderr, "error: invalid compaction: compact_at=%g context_window=%d (compact_at is a fraction of the window, 0 disables)\n", compactAt, window)
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
	if cfg.resume {
		if cfg.session == "" {
			fmt.Fprintln(os.Stderr, "error: --resume needs --session FILE")
			os.Exit(2)
		}
		if !state.OpenRun {
			fmt.Fprintln(os.Stderr, "error: session has no interrupted run to resume")
			os.Exit(2)
		}
		prompt = []string{resumePrompt(state)}
		printJSON(map[string]any{
			"type":        axproto.TypeResume,
			"run":         state.LastRunID,
			"interrupted": state.Interrupted,
		})
	} else if state.OpenRun {
		fmt.Fprintf(os.Stderr, "warning: previous run did not finish; repaired %d interrupted tool call(s)\n", state.Repaired)
	}

	detail := firstNonEmpty(cfg.imageDetail, fc.imageDetail, "auto")
	images, err := loadImages(cfg.images, detail, cfg.session)
	if err != nil {
		eventFailure(err.Error())
	}

	var (
		cat   *catalog.Catalog
		tools []Tool
	)
	if strings.EqualFold(firstNonEmpty(cfg.toolMode, fc.toolMode), "direct") {
		tools = toolsOrExit(fc, bot.Workspace)
	} else {
		sources, err := buildSources(fc, bot, false)
		if err != nil {
			eventFailure(err.Error())
		}
		cat = catalog.New(sources)
		loadCtx, cancelLoad := context.WithTimeout(context.Background(), catalogLoadTimeout)
		cat.Load(loadCtx)
		cancelLoad()
		tools = catalogTools(cat)
	}
	closeCatalog := func() {
		if cat != nil {
			_ = cat.Close()
			cat = nil
		}
	}
	defer closeCatalog()
	compactAt, contextWindow := resolveCompact(cfg, fc)
	system := resolveSystem(cfg, tools, bot, cat, coreToolIDs(fc, cat), compactAt, contextWindow)
	baseSystem := system
	if state.Summary != "" {
		system += summarySection(state.Summary)
	}
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
			closeCatalog()
			eventFailure(err.Error())
		}
		if err := log.append(sessionEntry{Type: entryMessage, RunID: runID, Message: &userMessage, Resume: cfg.resume}); err != nil {
			_ = log.close()
			closeCatalog()
			eventFailure(err.Error())
		}
	}

	var cancel atomic.Bool
	steer := make(chan string, 8)
	if !stdinIsTerminal() {
		secrets.enable()
		go func() {
			readControls(&cancel, steer)
			secrets.hostGone()
		}()
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
	var compact func(context.Context, []Message, bool) ([]Message, string, error)
	if compactAt > 0 && contextWindow > 0 {
		summarizer := NewOpenAI(base, apiKey(fc, bot))
		summarizer.SessionPath = cfg.session
		thinking := false
		summarizer.Thinking = &thinking
		summarizer.Effort = "none"
		summarizer.MaxTokens = compactSummaryTokens
		engine := &compactor{
			provider: summarizer,
			model:    model,
			base:     baseSystem,
			summary:  state.Summary,
			log:      log,
			runID:    runID,
			cancel:   &cancel,
			emit:     printJSON,
		}
		compact = engine.compact
	}
	end := runStream(context.Background(), provider, &runOptions{
		model:         model,
		system:        system,
		tools:         tools,
		maxTurns:      int(^uint(0) >> 1),
		compactAt:     compactAt,
		contextWindow: contextWindow,
		compact:       compact,
		steer:         steer,
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
			closeCatalog()
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
		closeCatalog()
		os.Exit(1)
	}
}

// resolveCompact merges the compaction settings: flags, then bot.toml, then
// the defaults copied from pixi (half of a one-million-token window).
func resolveCompact(cfg cliConfig, fc fileConfig) (float64, int) {
	compactAt, window := defaultCompactAt, defaultContextWindow
	if fc.compactAtSet {
		compactAt = fc.compactAt
	}
	if fc.contextWindowSet {
		window = fc.contextWindow
	}
	if cfg.compactAtSet {
		compactAt = cfg.compactAt
	}
	if cfg.contextWindowSet {
		window = cfg.contextWindow
	}
	return compactAt, window
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

// maxCoreTools caps the cheat sheet in the system prompt. A compute connection
// that exposes twenty tools should not turn the prompt into a tool list.
const maxCoreTools = 8

// coreToolIDs is what the prompt suggests calling without a search: the
// explicit core_tools, else every tool of the compute connection(s), else the
// historical fsx/bashx ids.
func coreToolIDs(fc fileConfig, cat *catalog.Catalog) []string {
	if len(fc.coreTools) > 0 {
		return fc.coreTools
	}
	if cat != nil && len(fc.computeIDs) > 0 {
		ids := make([]string, 0, maxCoreTools)
		for _, tool := range cat.Tools() {
			for _, id := range fc.computeIDs {
				if strings.EqualFold(tool.Server, id) {
					ids = append(ids, tool.ID)
					break
				}
			}
			if len(ids) == maxCoreTools {
				break
			}
		}
		if len(ids) > 0 {
			return ids
		}
	}
	return defaultCoreTools
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

// runtimeInfo is what ax knows about itself and the process it is running in.
// The system prompt gets a compact version of it; the values come from flags,
// the build and the transport's environment, never from secrets.
type runtimeInfo struct {
	Build         string
	Executable    string
	Module        string
	GOOS          string
	GOARCH        string
	GoVersion     string
	Transport     string
	Supervisor    string
	BotRoot       string
	DataRoot      string
	ChannelID     string
	ChannelName   string
	ThreadID      string
	ThreadTitle   string
	User          string
	Model         string
	BaseURL       string
	ContextWindow int
	CompactAt     float64
	ToolMode      string
	Session       string
	Now           time.Time
}

// axBuildID prefers the build the host activated (boti sets AX_BUILD), then
// the VCS revision Go embeds when the binary was built from a checkout, and
// finally "dev".
func axBuildID() string {
	if v := strings.TrimSpace(os.Getenv("AX_BUILD")); v != "" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				if len(setting.Value) > 7 {
					return setting.Value[:7]
				}
				return setting.Value
			}
		}
	}
	return "dev"
}

func newRuntimeInfo(cfg cliConfig, bot *botCtx, compactAt float64, contextWindow int) runtimeInfo {
	rt := runtimeInfo{
		Build:         axBuildID(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		GoVersion:     runtime.Version(),
		Transport:     "cli",
		Supervisor:    "standalone",
		Model:         cfg.model,
		BaseURL:       cfg.base,
		ContextWindow: contextWindow,
		CompactAt:     compactAt,
		ToolMode:      cfg.toolMode,
		Session:       cfg.session,
		Now:           time.Now().UTC(),
	}
	if bot != nil {
		rt.BotRoot = bot.Root
		rt.DataRoot = bot.DataRoot
	}
	if exe, err := os.Executable(); err == nil {
		rt.Executable = exe
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		rt.Module = info.Main.Path
	}
	if id := strings.TrimSpace(os.Getenv("AX_WEB_CHANNEL")); id != "" {
		rt.ChannelID = id
		rt.Transport = "webi"
	}
	rt.ChannelName = strings.TrimSpace(os.Getenv("AX_WEB_CHANNEL_NAME"))
	rt.ThreadID = strings.TrimSpace(os.Getenv("AX_WEB_THREAD"))
	rt.ThreadTitle = strings.TrimSpace(os.Getenv("AX_WEB_THREAD_TITLE"))
	rt.User = strings.TrimSpace(os.Getenv("AX_WEB_USER"))
	if strings.TrimSpace(os.Getenv("AX_HOST_SOCK")) != "" {
		rt.Supervisor = "boti"
	}
	if v := strings.TrimSpace(os.Getenv("AX_TRANSPORT")); v != "" {
		rt.Transport = v
	}
	return rt
}

// channelInstructions is the per-channel prompt webi stores. It lives in the
// environment because the channel is a transport concept; an empty value means
// the channel has no extra instructions.
func channelInstructions() string {
	return strings.TrimSpace(os.Getenv("AX_WEB_PROMPT"))
}

func runtimeSection(rt runtimeInfo) string {
	var out strings.Builder
	out.WriteString("\n\n## Runtime\n")
	fmt.Fprintf(&out, "You are ax, the assistant running on the AX engine (build %s, %s/%s, %s).\n",
		firstNonEmpty(rt.Build, "dev"), firstNonEmpty(rt.GOOS, "unknown"), firstNonEmpty(rt.GOARCH, "unknown"), firstNonEmpty(rt.GoVersion, "unknown"))
	if rt.Executable != "" {
		fmt.Fprintf(&out, "Binary: %s.\n", rt.Executable)
	}
	if rt.Transport != "" {
		fmt.Fprintf(&out, "Transport: %s", rt.Transport)
		if rt.Supervisor != "" {
			fmt.Fprintf(&out, "; supervisor: %s", rt.Supervisor)
		}
		out.WriteString(".\n")
	}
	if rt.BotRoot != "" {
		fmt.Fprintf(&out, "Bot root: %s; data root: %s.\n", rt.BotRoot, rt.DataRoot)
	}
	if rt.ChannelID != "" {
		channel := rt.ChannelID
		if rt.ChannelName != "" {
			channel = rt.ChannelName + " (" + rt.ChannelID + ")"
		}
		if rt.ThreadID != "" {
			thread := rt.ThreadID
			if rt.ThreadTitle != "" {
				thread = rt.ThreadTitle + " (" + rt.ThreadID + ")"
			}
			fmt.Fprintf(&out, "Channel: %s; thread: %s.\n", channel, thread)
		} else {
			fmt.Fprintf(&out, "Channel: %s.\n", channel)
		}
	}
	if rt.User != "" {
		fmt.Fprintf(&out, "User: %s.\n", rt.User)
	}
	if rt.Model != "" {
		fmt.Fprintf(&out, "Model: %s", rt.Model)
		if rt.BaseURL != "" {
			fmt.Fprintf(&out, " at %s", rt.BaseURL)
		}
		if rt.ContextWindow > 0 {
			fmt.Fprintf(&out, "; context %d tokens", rt.ContextWindow)
		}
		if rt.CompactAt > 0 {
			fmt.Fprintf(&out, " (compact at %.0f%%)", rt.CompactAt*100)
		}
		if rt.ToolMode != "" {
			fmt.Fprintf(&out, "; tools: %s", rt.ToolMode)
		}
		out.WriteString(".\n")
	}
	if rt.Session != "" {
		fmt.Fprintf(&out, "Session: %s.\n", rt.Session)
	}
	if rt.Module != "" {
		fmt.Fprintf(&out, "Source module: %s (checkout: the repo in bot.toml [boti.serve], if set).\n", rt.Module)
	}
	now := rt.Now
	if now.IsZero() {
		now = time.Now()
	}
	fmt.Fprintf(&out, "Time: %s. Use tools to check live state before guessing.\n", now.UTC().Format(time.RFC3339))
	return out.String()
}

func resolveSystem(cfg cliConfig, tools []Tool, bot *botCtx, cat *catalog.Catalog, core []string, compactAt float64, contextWindow int) string {
	if cfg.system != "" {
		return cfg.system
	}
	rt := newRuntimeInfo(cfg, bot, compactAt, contextWindow)
	if cat != nil {
		return buildCatalogSystemPrompt(bot.Workspace, bot, cat, core, rt)
	}
	return buildSystemPrompt(tools, bot.Workspace, bot, rt)
}

func runtimeOrEnv(rt []runtimeInfo, bot *botCtx) runtimeInfo {
	if len(rt) > 0 {
		return rt[0]
	}
	return newRuntimeInfo(cliConfig{}, bot, defaultCompactAt, defaultContextWindow)
}

func buildCatalogSystemPrompt(dir string, bot *botCtx, cat *catalog.Catalog, core []string, rt ...runtimeInfo) string {
	var out strings.Builder
	out.WriteString("You are an expert assistant operating inside ax. Use tools to find facts; do not guess.\n\n")
	fmt.Fprintf(&out, "Current working directory: %s\n", dir)
	out.WriteString(cat.Brief(core))
	out.WriteString("\nRules:\n")
	out.WriteString("- Answer in the user's language; be concise and direct.\n")
	if _, ok := cat.Lookup("memoryx.search"); ok {
		out.WriteString("- Check memory for personal facts and preferences when relevant.\n")
	}
	out.WriteString("- Verify changes before reporting success.\n")
	out.WriteString("- If a tool fails, fix the cause; do not repeat the same call.\n")
	out.WriteString("- There is no sandbox: commands run as the user. Never print or transmit secrets. Ask before destructive or irreversible actions.\n")
	out.WriteString("- If a request is ambiguous, assume and state it; ask only if blocked.\n")
	if edit := coreEditTool(core, cat); edit != "" {
		fmt.Fprintf(&out, "- Use %s for precise file changes; every edits[].oldText must match one unique region.\n", edit)
	}
	if bot != nil && bot.Prompt != "" {
		out.WriteString("\n## Bot instructions\nFollow these instructions exactly.\n")
		out.WriteString(bot.Prompt)
	}
	if up := userSystemPrompt(); up != "" {
		out.WriteString("\n\n## User instructions\n")
		out.WriteString(up)
	}
	if ci := channelInstructions(); ci != "" {
		out.WriteString("\n\n## Channel instructions\n")
		out.WriteString(ci)
	}
	out.WriteString(runtimeSection(runtimeOrEnv(rt, bot)))
	return out.String()
}

// coreEditTool names the editor the prompt should recommend, so a connection
// that renames fsx keeps the precise-edit hint.
func coreEditTool(core []string, cat *catalog.Catalog) string {
	for _, id := range core {
		if strings.HasSuffix(id, ".edit") {
			if _, ok := cat.Lookup(id); ok {
				return id
			}
		}
	}
	return ""
}

func buildSystemPrompt(tools []Tool, dir string, bot *botCtx, rt ...runtimeInfo) string {
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
	if ci := channelInstructions(); ci != "" {
		out.WriteString("\n\n## Channel instructions\n")
		out.WriteString(ci)
	}
	out.WriteString(runtimeSection(runtimeOrEnv(rt, bot)))
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
		case "tool_mode":
			c.toolMode = val
		case "core_tools":
			c.coreTools = strings.Fields(strings.ReplaceAll(val, ",", " "))
		case "compact_at":
			if value, err := strconv.ParseFloat(val, 64); err == nil && value >= 0 && value < 1 {
				c.compactAt, c.compactAtSet = value, true
			}
		case "context_window":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				c.contextWindow, c.contextWindowSet = n, true
			}
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

// readControls consumes the JSONL control channel on stdin: cancel stops the
// run at the next safe point, steer adds a user message at the next turn
// boundary.
func readControls(cancel *atomic.Bool, steer chan<- string) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var control struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Name  string `json:"name"`
			Value string `json:"value"`
			Skip  bool   `json:"skip"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &control); err != nil {
			continue
		}
		switch control.Type {
		case "secret":
			if control.Skip {
				secrets.skip(control.Name)
			} else {
				secrets.deliver(control.Name, control.Value)
			}
		case "cancel":
			cancel.Store(true)
		case "steer":
			if strings.TrimSpace(control.Text) == "" {
				continue
			}
			select {
			case steer <- control.Text:
			default:
				// The loop drains between turns; a burst past the buffer is
				// better dropped than blocking the control reader.
			}
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
	targets          map[string]string
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
	target := toolTarget(call)
	if target != "" {
		if s.targets == nil {
			s.targets = map[string]string{}
		}
		s.targets[call.ID] = target
	}
	s.calls[call.ID] = call
	s.append(sessionEntry{Type: entryToolIntent, Turn: s.turn, Call: &call, Target: target})
	event := map[string]any{"type": axproto.TypeToolStart, "id": call.ID, "name": call.Name, "arguments": call.Arguments}
	if target != "" {
		event["target"] = target
	}
	s.emit(event)
}

func toolTarget(call ToolCall) string {
	if call.Name != "execute" {
		return ""
	}
	var request struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &request); err != nil {
		return ""
	}
	return request.Tool
}

func (s *eventSink) toolDelta(call ToolCall, text string) {
	s.emit(map[string]any{"type": axproto.TypeToolDelta, "id": call.ID, "text": text})
}

func (s *eventSink) toolResult(call ToolCall) {
	s.emit(map[string]any{"type": axproto.TypeToolDone, "id": call.ID})
}

// steer records a message typed while the run was going. It belongs to the
// same run, so the session log keeps it in order and a resume replays it.
func (s *eventSink) steer(msg Message) {
	s.append(sessionEntry{Type: entryMessage, Message: &msg})
	s.emit(map[string]any{"type": axproto.TypeSteer, "text": msg.Content})
}

// secretRequest tells the host a tool needs a value. The model never sees it:
// the call is retried once the value exists.
func (s *eventSink) secretRequest(call ToolCall, ask secretAsk) {
	target := s.targets[call.ID]
	if target == "" {
		target = call.Name
	}
	s.emit(map[string]any{
		"type": axproto.TypeSecretRequest, "id": call.ID,
		"tool": target, "name": ask.Name, "description": ask.Description,
	})
}

func (s *eventSink) tokens(input, output, cached int) {
	u := Usage{Input: input, Output: output, CachedInput: cached}
	s.lastUsage = &u
	s.emit(map[string]any{"type": axproto.TypeUsage, "input": input, "output": output, "cached_input": cached, "model": s.model})
}

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
	s.append(sessionEntry{Type: entryToolResult, Turn: turn, Call: &call, Target: s.targets[msg.ToolCallID], Output: result.Content})
	s.emit(map[string]any{"type": axproto.TypeToolResult, "id": msg.ToolCallID, "output": msg.Content})
	s.emit(map[string]any{"type": axproto.TypeMessage, "message": msg})
}
