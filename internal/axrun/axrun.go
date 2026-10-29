package axrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Config struct {
	AXPath  string
	BotRoot string
	BaseURL string
	Model   string
	System  string
	Env     []string
}

type Job struct {
	Session string
	Prompt  string
	Images  []string
	Resume  bool
}

type Event struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Target       string          `json:"target,omitempty"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	Output       string          `json:"-"`
	OutputTokens int             `json:"-"`
	Message      json.RawMessage `json:"message,omitempty"`
	Messages     json.RawMessage `json:"messages,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	InputTokens  int             `json:"input,omitempty"`
	Outcome      string          `json:"outcome,omitempty"`
	Summary      string          `json:"summary,omitempty"`
	Upto         int             `json:"upto,omitempty"`
	Description  string          `json:"description,omitempty"`
	Raw          json.RawMessage `json:"-"`
}

func (e *Event) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type        string          `json:"type"`
		Text        string          `json:"text"`
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Target      string          `json:"target"`
		Arguments   json.RawMessage `json:"arguments"`
		Output      json.RawMessage `json:"output"`
		Message     json.RawMessage `json:"message"`
		Messages    json.RawMessage `json:"messages"`
		Usage       *Usage          `json:"usage"`
		Input       int             `json:"input"`
		Outcome     string          `json:"outcome"`
		Summary     string          `json:"summary"`
		Description string          `json:"description"`
		Upto        int             `json:"upto"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*e = Event{
		Type: raw.Type, Text: raw.Text, ID: raw.ID, Name: raw.Name, Target: raw.Target,
		Arguments: raw.Arguments, Message: raw.Message, Messages: raw.Messages,
		Usage: raw.Usage, InputTokens: raw.Input, Outcome: raw.Outcome,
		Summary: raw.Summary, Upto: raw.Upto, Description: raw.Description,
	}
	if len(raw.Output) > 0 {
		var text string
		if err := json.Unmarshal(raw.Output, &text); err == nil {
			e.Output = text
		} else {
			_ = json.Unmarshal(raw.Output, &e.OutputTokens)
		}
	}
	return nil
}

type Usage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CachedInput int `json:"cached_input"`
}

func (e Event) ErrorMessage() string {
	var text string
	if err := json.Unmarshal(e.Message, &text); err == nil {
		return text
	}
	return strings.TrimSpace(string(e.Message))
}

func (e Event) MessageBody() (Message, bool) {
	var msg Message
	if len(e.Message) == 0 || json.Unmarshal(e.Message, &msg) != nil {
		return Message{}, false
	}
	return msg, true
}

type Message struct {
	Role       string          `json:"Role"`
	Content    string          `json:"Content"`
	ToolCalls  json.RawMessage `json:"ToolCalls,omitempty"`
	ToolCallID string          `json:"ToolCallID,omitempty"`
}

type Result struct {
	Text     string
	Outcome  string
	Failure  string
	Usage    Usage
	Messages []Message
}

type Run struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *strings.Builder

	mu   sync.Mutex
	done bool

	events chan Event
	wait   chan struct{}
	err    error
}

func Start(ctx context.Context, cfg Config, job Job, onEvent func(Event)) (*Run, error) {
	if strings.TrimSpace(job.Prompt) == "" && !job.Resume {
		return nil, errors.New("axrun: empty prompt")
	}
	if job.Resume && job.Session == "" {
		return nil, errors.New("axrun: resume needs a session")
	}
	path := cfg.AXPath
	if path == "" {
		path = "ax"
	}
	args := []string{"--events"}
	if job.Session != "" {
		args = append(args, "--session", job.Session)
	}
	if job.Resume {
		args = append(args, "--resume")
	}
	if cfg.BotRoot != "" {
		args = append(args, "-C", cfg.BotRoot)
	}
	if cfg.BaseURL != "" {
		args = append(args, "-base", cfg.BaseURL)
	}
	if cfg.Model != "" {
		args = append(args, "-model", cfg.Model)
	}
	if cfg.System != "" {
		args = append(args, "-system", cfg.System)
	}
	for _, image := range job.Images {
		args = append(args, "-image", image)
	}
	if !job.Resume {
		args = append(args, job.Prompt)
	}
	return start(ctx, path, args, cfg.Env, onEvent)
}

func Compact(ctx context.Context, cfg Config, session string, onEvent func(Event)) (Result, error) {
	if strings.TrimSpace(session) == "" {
		return Result{}, errors.New("axrun: empty session")
	}
	path := cfg.AXPath
	if path == "" {
		path = "ax"
	}
	args := []string{"compact", "--session", session}
	if cfg.BotRoot != "" {
		args = append(args, "-C", cfg.BotRoot)
	}
	if cfg.BaseURL != "" {
		args = append(args, "-base", cfg.BaseURL)
	}
	if cfg.Model != "" {
		args = append(args, "-model", cfg.Model)
	}
	run, err := start(ctx, path, args, cfg.Env, onEvent)
	if err != nil {
		return Result{}, err
	}
	return run.Wait()
}

func start(ctx context.Context, path string, args, env []string, onEvent func(Event)) (*Run, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ax: %w", err)
	}
	run := &Run{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: &stderr,
		events: make(chan Event, 64),
		wait:   make(chan struct{}),
	}
	go run.pump(onEvent)
	return run, nil
}

func (r *Run) pump(onEvent func(Event)) {
	defer close(r.events)
	defer close(r.wait)
	scanner := bufio.NewScanner(r.stdout)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Type == "" {
			continue
		}
		event.Raw = json.RawMessage(line)
		if onEvent != nil {
			onEvent(event)
		}
		select {
		case r.events <- event:
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		r.err = err
	}
}

func (r *Run) Events() <-chan Event { return r.events }

func (r *Run) Steer(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("axrun: empty steer")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return errors.New("axrun: run already finished")
	}
	payload, err := json.Marshal(map[string]string{"type": "steer", "text": text})
	if err != nil {
		return err
	}
	_, err = r.stdin.Write(append(payload, '\n'))
	return err
}

func (r *Run) Secret(name, value string) error {
	return r.control(map[string]any{"type": "secret", "name": name, "value": value})
}

func (r *Run) SkipSecret(name string) error {
	return r.control(map[string]any{"type": "secret", "name": name, "skip": true})
}

func (r *Run) control(payload map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return errors.New("axrun: run already finished")
	}
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = r.stdin.Write(append(line, '\n'))
	return err
}

func (r *Run) Cancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return errors.New("axrun: run already finished")
	}
	_, err := io.WriteString(r.stdin, `{"type":"cancel"}`+"\n")
	return err
}

func (r *Run) Wait() (Result, error) {
	<-r.wait
	_ = r.stdin.Close()
	waitErr := r.cmd.Wait()
	r.mu.Lock()
	r.done = true
	r.mu.Unlock()

	result := Result{}
	for {
		select {
		case event, ok := <-r.events:
			if !ok {
				goto done
			}
			applyEvent(&result, event)
		default:
			goto done
		}
	}
done:
	if r.err != nil {
		return result, r.err
	}
	if result.Failure != "" {
		return result, errors.New(result.Failure)
	}
	if waitErr != nil {
		message := strings.TrimSpace(r.stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return result, errors.New(message)
	}
	switch result.Outcome {
	case "done", "compact", "cancelled":
		return result, nil
	case "":
		return result, errors.New("ax ended without an outcome")
	default:
		return result, fmt.Errorf("ax ended with %s", result.Outcome)
	}
}

func applyEvent(result *Result, event Event) {
	switch event.Type {
	case "error":
		result.Failure = event.ErrorMessage()
	case "done":
		result.Outcome = event.Outcome
	case "usage":
		if event.Usage != nil {
			result.Usage = *event.Usage
		} else {
			result.Usage = Usage{Input: event.InputTokens, Output: event.OutputTokens}
		}
	case "result":
		applyResult(result, event.Messages)
	}
}

func applyResult(result *Result, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var messages []Message
	if err := json.Unmarshal(raw, &messages); err != nil {
		return
	}
	result.Messages = messages
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "assistant" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if calls := strings.TrimSpace(string(msg.ToolCalls)); calls != "" && calls != "null" && calls != "[]" {
			continue
		}
		result.Text = strings.TrimSpace(msg.Content)
		return
	}
}

func RunOne(ctx context.Context, cfg Config, job Job, onEvent func(Event)) (Result, error) {
	run, err := Start(ctx, cfg, job, onEvent)
	if err != nil {
		return Result{}, err
	}
	return run.Wait()
}

func (r *Run) Kill() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done {
		return
	}
	_ = r.stdin.Close()
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	time.Sleep(10 * time.Millisecond)
}
