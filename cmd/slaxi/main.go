package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/3-lines-studio/ax/internal/axrun"
	"github.com/3-lines-studio/ax/internal/botdir"
)

type config struct {
	appToken    string
	botToken    string
	axPath      string
	botRoot     string
	workspace   string
	stateAX     string
	stateSlack  string
	runSlack    string
	artifacts   string
	model       string
	baseURL     string
	system      string
	mentionOnly bool
}

type socketOpen struct {
	OK    bool   `json:"ok"`
	URL   string `json:"url"`
	Error string `json:"error"`
}

type envelope struct {
	EnvelopeID   string          `json:"envelope_id"`
	Type         string          `json:"type"`
	AcceptsReply bool            `json:"accepts_response_payload"`
	Payload      json.RawMessage `json:"payload"`
}

type eventPayload struct {
	TeamID         string          `json:"team_id"`
	Event          slackEvent      `json:"event"`
	Authorizations []authorization `json:"authorizations"`
}

type slackEvent struct {
	Type        string `json:"type"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	Subtype     string `json:"subtype"`
	BotID       string `json:"bot_id"`
}

type authorization struct {
	UserID string `json:"user_id"`
}

type job struct {
	teamID   string
	channel  string
	threadTS string
	prompt   string
	isDM     bool
}

type slackResponse struct {
	OK    bool   `json:"ok"`
	TS    string `json:"ts"`
	Error string `json:"error"`
}

var markdownLink = regexp.MustCompile(`\[([^]]+)\]\((https?://[^ )]+)\)`)
var markdownBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
var markdownUnderlineBold = regexp.MustCompile(`__([^_]+)__`)
var markdownStrike = regexp.MustCompile(`~~([^~]+)~~`)
var tableDelimiter = regexp.MustCompile(`^:?-{3,}:?$`)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		os.Exit(doctor())
	}
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("slaxi: config", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobs := make(chan job, 64)
	seen := make(map[string]struct{})
	go work(cfg, jobs)
	backoff := time.Second
	for ctx.Err() == nil {
		err := serve(ctx, cfg, jobs, seen)
		if ctx.Err() != nil {
			break
		}
		slog.Warn("slaxi: slack connection lost", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func loadConfig() (config, error) {
	cfg := config{
		axPath:  os.Getenv("SLAXI_AX_PATH"),
		model:   os.Getenv("SLAXI_MODEL"),
		baseURL: os.Getenv("SLAXI_BASE_URL"),
	}
	botRoot := os.Getenv("BOT_ROOT")
	if botRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return cfg, fmt.Errorf("working directory: %w", err)
		}
		botRoot = wd
	}
	if absolute, err := filepath.Abs(botRoot); err == nil {
		botRoot = absolute
	}
	cfg.botRoot = botRoot

	bc, err := loadBotConfig(botRoot)
	if err != nil {
		return cfg, err
	}
	if cfg.model == "" {
		cfg.model = bc.Model
	}
	if cfg.baseURL == "" {
		cfg.baseURL = bc.BaseURL
	}
	cfg.mentionOnly = bc.Slack.MentionOnly

	appToken, err := botdir.ReadSecret(botRoot, "SLACK_APP_TOKEN", "slack-app-token")
	if err != nil {
		return cfg, err
	}
	if appToken == "" {
		return cfg, errors.New("slack app token is missing: set SLACK_APP_TOKEN or secrets/slack-app-token")
	}
	cfg.appToken = appToken
	botToken, err := botdir.ReadSecret(botRoot, "SLACK_BOT_TOKEN", "slack-bot-token")
	if err != nil {
		return cfg, err
	}
	if botToken == "" {
		return cfg, errors.New("slack bot token is missing: set SLACK_BOT_TOKEN or secrets/slack-bot-token")
	}
	cfg.botToken = botToken

	if os.Getenv("OPENAI_API_KEY") == "" && !botdir.Exists(filepath.Join(botRoot, "secrets", "api-key")) {
		return cfg, errors.New("model api key is missing: set OPENAI_API_KEY or secrets/api-key")
	}

	if cfg.axPath == "" {
		cfg.axPath = "ax"
	}
	path, err := exec.LookPath(cfg.axPath)
	if err != nil {
		return cfg, fmt.Errorf("find ax: %w", err)
	}
	cfg.axPath = path

	runtimeRoot := os.Getenv("BOT_DATA")
	if runtimeRoot == "" {
		runtimeRoot = botRoot
	}
	cfg.workspace = filepath.Join(runtimeRoot, "workspace")
	cfg.stateAX = filepath.Join(runtimeRoot, "state", "ax", "sessions")
	cfg.stateSlack = filepath.Join(runtimeRoot, "state", "slack")
	cfg.runSlack = filepath.Join(runtimeRoot, "run", "slack")
	cfg.artifacts = filepath.Join(runtimeRoot, "workspace", "slack", "artifacts")
	for _, dir := range []string{cfg.workspace, cfg.stateAX, cfg.stateSlack, cfg.runSlack, cfg.artifacts} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return cfg, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return cfg, nil
}

func serve(ctx context.Context, cfg config, jobs chan<- job, seen map[string]struct{}) error {
	socketURL, err := openSocket(ctx, cfg)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, socketURL, nil)
	if err != nil {
		return fmt.Errorf("connect socket: %w", err)
	}
	defer func() { _ = conn.CloseNow() }()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read socket: %w", err)
		}
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Warn("slaxi: malformed envelope", "error", err)
			continue
		}
		if env.EnvelopeID == "" {
			continue
		}
		ack, err := json.Marshal(struct {
			EnvelopeID string `json:"envelope_id"`
		}{env.EnvelopeID})
		if err != nil {
			slog.Warn("slaxi: encode ack", "error", err)
			continue
		}
		if err := conn.Write(ctx, websocket.MessageText, ack); err != nil {
			return fmt.Errorf("ack event: %w", err)
		}
		if _, ok := seen[env.EnvelopeID]; ok {
			continue
		}
		if len(seen) >= 10000 {
			clear(seen)
		}
		seen[env.EnvelopeID] = struct{}{}
		if env.Type != "events_api" {
			continue
		}
		j, ok := parseJob(env.Payload)
		if !ok {
			continue
		}
		if cfg.mentionOnly && j.isDM {
			continue
		}
		select {
		case jobs <- j:
		default:
			slog.Warn("slaxi: drop event", "id", env.EnvelopeID, "reason", "queue full")
		}
	}
}

var slackClient = &http.Client{Timeout: 30 * time.Second}

func openSocket(ctx context.Context, cfg config) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.appToken)
	resp, err := slackClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("open socket: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var result socketOpen
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("decode socket response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("open socket: %s", result.Error)
	}
	return result.URL, nil
}

func parseJob(data []byte) (job, bool) {
	var payload eventPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Warn("slaxi: malformed event payload", "error", err)
		return job{}, false
	}
	event := payload.Event
	isMention := event.Type == "app_mention"
	isDM := event.Type == "message" && event.ChannelType == "im" && event.Subtype == "" && event.BotID == ""
	if !isMention && !isDM {
		return job{}, false
	}
	threadTS := payload.Event.ThreadTS
	if threadTS == "" {
		threadTS = payload.Event.TS
	}
	if !safePart(payload.TeamID) || !safePart(payload.Event.Channel) || !safePart(threadTS) {
		return job{}, false
	}
	prompt := payload.Event.Text
	if len(payload.Authorizations) > 0 {
		prompt = strings.ReplaceAll(prompt, "<@"+payload.Authorizations[0].UserID+">", "")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return job{}, false
	}
	return job{teamID: payload.TeamID, channel: payload.Event.Channel, threadTS: threadTS, prompt: prompt, isDM: isDM}, true
}

func safePart(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return value != "." && value != ".."
}

func work(cfg config, jobs <-chan job) {
	for j := range jobs {
		messageTS, err := postMessage(cfg, j, "Working…")
		if err != nil {
			slog.Error("slaxi: progress update", "error", err)
		}
		reply, err := executeJob(cfg, j)
		if err != nil {
			slog.Error("slaxi: ax run", "error", err)
			reply = "AX failed: " + err.Error()
		}
		parts := splitMessage(formatMarkdown(reply), 3500)
		if messageTS != "" {
			err = updateMessage(cfg, j.channel, messageTS, parts[0])
		} else {
			_, err = postMessage(cfg, j, parts[0])
		}
		if err != nil {
			slog.Error("slaxi: reply", "error", err)
			continue
		}
		for _, part := range parts[1:] {
			if _, err := postMessage(cfg, j, part); err != nil {
				slog.Error("slaxi: reply", "error", err)
				break
			}
		}
	}
}

func executeJob(cfg config, j job) (string, error) {
	return runAX(cfg, j)
}

// runAX is the whole engine contract of this transport: one ax process per
// message, its NDJSON events folded into the reply Slack shows. internal/axrun
// does the same for webi, so both transports behave alike.
func runAX(cfg config, j job) (string, error) {
	result, err := axrun.RunOne(context.Background(), axrun.Config{
		AXPath:  cfg.axPath,
		BotRoot: cfg.botRoot,
		BaseURL: cfg.baseURL,
		Model:   cfg.model,
		System:  cfg.system,
		Env: []string{
			"AX_SLACK_CHANNEL=" + j.channel,
			"AX_SLACK_THREAD=" + j.threadTS,
			"AX_ARTIFACT_DIR=" + filepath.Join(cfg.artifacts, j.teamID, j.channel),
		},
	}, axrun.Job{
		Session: filepath.Join(cfg.stateAX, j.teamID, j.channel, j.threadTS+".jsonl"),
		Prompt:  j.prompt,
	}, nil)
	if err != nil {
		return "", err
	}
	if result.Outcome != "done" {
		return "", fmt.Errorf("ax ended with %s", result.Outcome)
	}
	if result.Text == "" {
		return "", errors.New("ax returned an empty response")
	}
	return result.Text, nil
}

func formatMarkdown(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var output []string
	inCode := false
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			output = append(output, line)
			i++
			continue
		}
		if !inCode && i+1 < len(lines) && isTableDelimiter(lines[i+1]) {
			table := [][]string{splitTable(line)}
			i += 2
			for i < len(lines) {
				row := splitTable(lines[i])
				if len(row) != len(table[0]) {
					break
				}
				table = append(table, row)
				i++
			}
			output = append(output, formatTable(table))
			continue
		}
		if !inCode {
			line = formatLine(line)
		}
		output = append(output, line)
		i++
	}
	return strings.Join(output, "\n")
}

func formatLine(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if strings.HasPrefix(trimmed, "#") {
		title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if title != "" {
			line = "*" + title + "*"
		}
	}
	line = markdownLink.ReplaceAllString(line, `<$2|$1>`)
	line = markdownBold.ReplaceAllString(line, `*$1*`)
	line = markdownUnderlineBold.ReplaceAllString(line, `*$1*`)
	line = markdownStrike.ReplaceAllString(line, `~$1~`)
	return line
}

func isTableDelimiter(line string) bool {
	cells := splitTable(line)
	if len(cells) < 2 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if !tableDelimiter.MatchString(cell) {
			return false
		}
	}
	return true
}

func splitTable(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func formatTable(rows [][]string) string {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len([]rune(cell)))
		}
	}
	var lines []string
	for rowIndex, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = cell + strings.Repeat(" ", widths[i]-len([]rune(cell)))
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
		if rowIndex == 0 {
			separators := make([]string, len(widths))
			for i, width := range widths {
				separators[i] = strings.Repeat("-", width)
			}
			lines = append(lines, "|-"+strings.Join(separators, "-|-")+"-|")
		}
	}
	return "```\n" + strings.Join(lines, "\n") + "\n```"
}

func splitMessage(text string, limit int) []string {
	if len([]rune(text)) <= limit {
		return []string{text}
	}
	blocks := strings.Split(text, "\n\n")
	var parts []string
	current := ""
	for _, block := range blocks {
		separator := ""
		if current != "" {
			separator = "\n\n"
		}
		if len([]rune(current+separator+block)) <= limit {
			current += separator + block
			continue
		}
		if current != "" {
			parts = append(parts, current)
		}
		for len([]rune(block)) > limit {
			runes := []rune(block)
			cut := limit
			for cut > limit/2 && runes[cut] != '\n' {
				cut--
			}
			if cut == limit/2 {
				cut = limit
			}
			parts = append(parts, strings.TrimSpace(string(runes[:cut])))
			block = strings.TrimSpace(string(runes[cut:]))
		}
		current = block
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func postMessage(cfg config, j job, text string) (string, error) {
	form := url.Values{
		"channel":   {j.channel},
		"thread_ts": {j.threadTS},
		"text":      {text},
	}
	result, err := slackAPI(cfg, "chat.postMessage", form)
	return result.TS, err
}

func updateMessage(cfg config, channel, ts, text string) error {
	form := url.Values{
		"channel": {channel},
		"ts":      {ts},
		"text":    {text},
	}
	_, err := slackAPI(cfg, "chat.update", form)
	return err
}

func slackAPI(cfg config, method string, form url.Values) (slackResponse, error) {
	var result slackResponse
	req, err := http.NewRequest(http.MethodPost, "https://slack.com/api/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.botToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := slackClient.Do(req)
	if err != nil {
		return result, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return result, err
	}
	if !result.OK {
		return result, errors.New(result.Error)
	}
	return result, nil
}
