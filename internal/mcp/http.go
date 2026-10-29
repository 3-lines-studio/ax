package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type httpTransport struct {
	cfg    ServerConfig
	client *http.Client

	mu        sync.Mutex
	protocol  string
	sessionID string
}

func newHTTPTransport(cfg ServerConfig) *httpTransport {
	return &httpTransport{cfg: cfg, client: &http.Client{}}
}

func (t *httpTransport) RoundTrip(ctx context.Context, request []byte) ([]byte, error) {
	resp, err := t.do(ctx, request, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && t.cfg.Auth != nil {
		_ = resp.Body.Close()
		resp, err = t.do(ctx, request, true)
		if err != nil {
			return nil, err
		}
	}
	defer func() { _ = resp.Body.Close() }()
	return readResponse(resp, t.cfg.Name)
}

func (t *httpTransport) do(ctx context.Context, request []byte, force bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.URL, bytes.NewReader(request))
	if err != nil {
		return nil, fmt.Errorf("mcp: server %q: request: %w", t.cfg.Name, err)
	}
	if err := t.headers(ctx, req, force); err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: server %q: %w", t.cfg.Name, err)
	}
	t.captureSession(resp)
	if resp.StatusCode == http.StatusUnauthorized && t.cfg.Auth == nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("mcp: server %q: authentication required; run ax mcp login %s", t.cfg.Name, t.cfg.Name)
	}
	return resp, nil
}

func (t *httpTransport) Notify(ctx context.Context, notification []byte) error {
	resp, err := t.do(ctx, notification, false)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp: server %q: notification failed with HTTP %d", t.cfg.Name, resp.StatusCode)
	}
	return nil
}

func (t *httpTransport) headers(ctx context.Context, req *http.Request, force bool) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "ax/"+Version)
	t.mu.Lock()
	protocol, session := t.protocol, t.sessionID
	t.mu.Unlock()
	if protocol != "" {
		req.Header.Set("Mcp-Protocol-Version", protocol)
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if t.cfg.Auth != nil {
		token, err := t.cfg.Auth.Token(ctx, force)
		if err != nil {
			if errors.Is(err, ErrLoginRequired) {
				return &AuthRequiredError{Server: t.cfg.Name}
			}
			return fmt.Errorf("mcp: server %q: %w", t.cfg.Name, err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	for key, value := range t.cfg.Headers {
		req.Header.Set(key, value)
	}
	return nil
}

func (t *httpTransport) captureSession(resp *http.Response) {
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		t.mu.Lock()
		t.sessionID = id
		t.mu.Unlock()
	}
}

func (t *httpTransport) SetProtocol(version string) {
	t.mu.Lock()
	t.protocol = version
	t.mu.Unlock()
}

func (t *httpTransport) SetSessionID(id string) {
	t.mu.Lock()
	t.sessionID = id
	t.mu.Unlock()
}

func (t *httpTransport) Close() error {
	t.client.CloseIdleConnections()
	return nil
}

func readResponse(resp *http.Response, name string) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var envelope struct {
			Error *RPCError `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error != nil {
			return nil, envelope.Error
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("mcp: server %q: HTTP %d: %s", name, resp.StatusCode, message)
	}
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return readEventStream(resp.Body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage+1))
	if err != nil {
		return nil, fmt.Errorf("mcp: server %q: read response: %w", name, err)
	}
	if len(body) > maxMessage {
		return nil, fmt.Errorf("mcp: server %q: response exceeds %d bytes", name, maxMessage)
	}
	return body, nil
}

func readEventStream(body io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxMessage)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if payload, ok := takeEvent(data); ok {
				return payload, nil
			}
			data = nil
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if payload, ok := takeEvent(data); ok {
		return payload, nil
	}
	return nil, fmt.Errorf("mcp: event stream ended without a response")
}

func takeEvent(data []string) ([]byte, bool) {
	if len(data) == 0 {
		return nil, false
	}
	payload := []byte(strings.Join(data, "\n"))
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, false
	}
	if len(envelope.Result) > 0 || envelope.Error != nil {
		return payload, true
	}
	return nil, false
}
