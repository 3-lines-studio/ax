package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxMessage = 32 << 20

type stdioTransport struct {
	cfg    ServerConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailWriter
	done   chan struct{}
	once   sync.Once

	writeMu sync.Mutex
	pendMu  sync.Mutex
	pending map[int64]chan []byte
	err     error
}

func newStdioTransport(cfg ServerConfig) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: server %q: stdin: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: server %q: stdout: %w", cfg.Name, err)
	}
	stderr := &tailWriter{limit: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start server %q: %w", cfg.Name, err)
	}
	t := &stdioTransport{
		cfg: cfg, cmd: cmd, stdin: stdin, stderr: stderr,
		done: make(chan struct{}), pending: map[int64]chan []byte{},
	}
	go t.readLoop(stdout)
	go func() {
		waitErr := cmd.Wait()
		t.finish(waitErr)
	}()
	return t, nil
}

func mergeEnv(base []string, overrides []string) []string {
	values := map[string]string{}
	order := make([]string, 0, len(base)+len(overrides))
	for _, entry := range append(append([]string{}, base...), overrides...) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}

func (t *stdioTransport) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxMessage)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		t.dispatch(line)
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	t.finish(err)
}

func (t *stdioTransport) dispatch(line []byte) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return
	}
	if envelope.Method != "" {
		if len(envelope.ID) > 0 && string(envelope.ID) != "null" {
			result := map[string]any{}
			if envelope.Method != "ping" {
				_ = t.writeObject(map[string]any{
					"jsonrpc": "2.0",
					"id":      envelope.ID,
					"error":   map[string]any{"code": -32601, "message": "method not supported by ax"},
				})
				return
			}
			_ = t.writeObject(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": result})
		}
		return
	}
	var id int64
	if err := json.Unmarshal(envelope.ID, &id); err != nil {
		return
	}
	t.pendMu.Lock()
	ch := t.pending[id]
	delete(t.pending, id)
	t.pendMu.Unlock()
	if ch != nil {
		ch <- line
	}
}

func (t *stdioTransport) finish(err error) {
	t.once.Do(func() {
		t.pendMu.Lock()
		t.err = err
		pending := t.pending
		t.pending = map[int64]chan []byte{}
		t.pendMu.Unlock()
		close(t.done)
		for _, ch := range pending {
			close(ch)
		}
	})
}

func (t *stdioTransport) RoundTrip(ctx context.Context, request []byte) ([]byte, error) {
	select {
	case <-t.done:
		return nil, t.closedError()
	default:
	}
	var envelope struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(request, &envelope); err != nil {
		return nil, fmt.Errorf("mcp: encode request id: %w", err)
	}
	ch := make(chan []byte, 1)
	t.pendMu.Lock()
	if _, exists := t.pending[envelope.ID]; exists {
		t.pendMu.Unlock()
		return nil, fmt.Errorf("mcp: duplicate request id %d", envelope.ID)
	}
	t.pending[envelope.ID] = ch
	t.pendMu.Unlock()

	if err := t.writeMessage(request); err != nil {
		t.pendMu.Lock()
		delete(t.pending, envelope.ID)
		t.pendMu.Unlock()
		return nil, err
	}
	select {
	case response, ok := <-ch:
		if !ok {
			return nil, t.closedError()
		}
		return response, nil
	case <-ctx.Done():
		t.pendMu.Lock()
		delete(t.pending, envelope.ID)
		t.pendMu.Unlock()
		return nil, ctx.Err()
	case <-t.done:
		return nil, t.closedError()
	}
}

func (t *stdioTransport) Notify(ctx context.Context, notification []byte) error {
	select {
	case <-t.done:
		return t.closedError()
	default:
	}
	return t.writeMessage(notification)
}

func (t *stdioTransport) writeObject(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return t.writeMessage(data)
}

func (t *stdioTransport) writeMessage(payload []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("mcp: write to server %q: %w", t.cfg.Name, err)
	}
	return nil
}

func (t *stdioTransport) Close() error {
	_ = t.stdin.Close()
	select {
	case <-t.done:
		return nil
	case <-time.After(2 * time.Second):
	}
	if t.cmd.Process != nil {
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	}
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
	}
	return nil
}

func (t *stdioTransport) SetProtocol(string)  {}
func (t *stdioTransport) SetSessionID(string) {}

func (t *stdioTransport) closedError() error {
	t.pendMu.Lock()
	err := t.err
	t.pendMu.Unlock()
	message := t.stderr.String()
	if strings.TrimSpace(message) != "" {
		return fmt.Errorf("mcp: server %q stopped: %v: %s", t.cfg.Name, err, strings.TrimSpace(message))
	}
	if err == nil {
		err = errors.New("connection closed")
	}
	return fmt.Errorf("mcp: server %q stopped: %w", t.cfg.Name, err)
}

type tailWriter struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		w.buf = w.buf[len(w.buf)-w.limit:]
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
