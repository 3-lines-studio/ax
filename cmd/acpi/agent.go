package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// agent implements acp.Agent by driving ax as a subprocess per turn. It
// holds no model/tool logic of its own -- that all lives in ax and in
// whatever AX_TOOLS the process that launches acpi has configured.
type agent struct {
	cfg  *Config
	conn *acp.AgentSideConnection

	mu       sync.Mutex
	sessions map[acp.SessionId]*session
}

func newAgent(cfg *Config) *agent {
	return &agent{cfg: cfg, sessions: make(map[acp.SessionId]*session)}
}

func (a *agent) SetAgentConnection(c *acp.AgentSideConnection) { a.conn = c }

func (a *agent) Initialize(_ context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "acpi", Version: version},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: false,
			// Text-only for v1: ax's own Message/content model is plain text,
			// and PromptRequest.Prompt image/audio/resource blocks aren't
			// wired through to ax yet (see promptText).
			PromptCapabilities: acp.PromptCapabilities{},
		},
	}, nil
}

func (a *agent) NewSession(_ context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if params.Cwd == "" {
		return acp.NewSessionResponse{}, fmt.Errorf("acpi: cwd is required")
	}
	id, err := newSessionID()
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err := os.MkdirAll(a.cfg.SessionDir, 0o755); err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("acpi: creating session dir: %w", err)
	}
	sess := &session{
		id:         acp.SessionId(id),
		cwd:        params.Cwd,
		axSession:  filepath.Join(a.cfg.SessionDir, id+".jsonl"),
		mcpServers: params.McpServers,
	}
	if len(params.McpServers) > 0 {
		// v1 doesn't bridge MCP servers into ax's tool loop yet (that's mcpx's
		// job). runAx still writes them out to <axSession>.mcp-servers.json so
		// mcpx has a file to read once it exists, but no tool is registered
		// for them today, so the agent won't be able to call them.
		slog.Warn("acpi: session has mcp servers, but the mcpx bridge is not wired up yet -- these tools will not be callable",
			"session", id, "servers", len(params.McpServers))
	}
	a.mu.Lock()
	a.sessions[sess.id] = sess
	a.mu.Unlock()
	return acp.NewSessionResponse{SessionId: sess.id}, nil
}

func (a *agent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	sess, ok := a.session(params.SessionId)
	if !ok {
		return acp.PromptResponse{}, fmt.Errorf("acpi: unknown session %q", params.SessionId)
	}
	text := promptText(params.Prompt)
	if text == "" {
		return acp.PromptResponse{}, fmt.Errorf("acpi: prompt has no text content (image/audio/resource blocks are not yet supported)")
	}
	return runAx(ctx, a.conn, a.cfg, sess, text)
}

func (a *agent) Cancel(_ context.Context, params acp.CancelNotification) error {
	sess, ok := a.session(params.SessionId)
	if !ok {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.active == nil {
		return nil
	}
	_, err := sess.active.stdin.Write([]byte(`{"type":"cancel"}` + "\n"))
	return err
}

func (a *agent) CloseSession(_ context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.mu.Lock()
	sess, ok := a.sessions[params.SessionId]
	delete(a.sessions, params.SessionId)
	a.mu.Unlock()
	if ok {
		sess.mu.Lock()
		if sess.active != nil {
			_ = sess.active.cmd.Process.Kill()
		}
		sess.mu.Unlock()
	}
	return acp.CloseSessionResponse{}, nil
}

func (a *agent) session(id acp.SessionId) (*session, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	return s, ok
}

func newSessionID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("acpi: generating session id: %w", err)
	}
	return "acpi_" + hex.EncodeToString(b), nil
}

// promptText flattens the text blocks of a prompt into one string for ax's
// positional prompt argument. Non-text blocks (image/audio/resource) are
// dropped -- see the PromptCapabilities note in Initialize.
func promptText(blocks []acp.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text == nil {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(b.Text.Text)
	}
	return sb.String()
}

// --- Rest of the acp.Agent interface. acpi doesn't support these in v1;
// each returns the SDK's zero-value response, same as the SDK's own example
// agent does for capabilities it doesn't implement. ---

func (a *agent) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *agent) Logout(_ context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (a *agent) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (a *agent) ResumeSession(_ context.Context, _ acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, fmt.Errorf("acpi: session resume is not supported")
}

func (a *agent) SetSessionConfigOption(_ context.Context, _ acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (a *agent) SetSessionMode(_ context.Context, _ acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}
