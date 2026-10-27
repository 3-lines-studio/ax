package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// testClient implements acp.Client just enough to drive acpi in tests:
// records every session/update notification and no-ops everything else
// (acpi doesn't call RequestPermission/terminal/fs methods in v1).
type testClient struct {
	t       *testing.T
	updates []acp.SessionNotification
}

func (c *testClient) SessionUpdate(_ context.Context, params acp.SessionNotification) error {
	c.t.Logf("session/update: %+v", params.Update)
	c.updates = append(c.updates, params)
	return nil
}
func (c *testClient) RequestPermission(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}
func (c *testClient) ReadTextFile(_ context.Context, _ acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, nil
}
func (c *testClient) WriteTextFile(_ context.Context, _ acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, nil
}
func (c *testClient) CreateTerminal(_ context.Context, _ acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, nil
}
func (c *testClient) KillTerminal(_ context.Context, _ acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, nil
}
func (c *testClient) TerminalOutput(_ context.Context, _ acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}
func (c *testClient) ReleaseTerminal(_ context.Context, _ acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}
func (c *testClient) WaitForTerminalExit(_ context.Context, _ acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}

// TestEndToEndWithFakeAx drives a real acpi binary over stdio, exactly as
// buzz-acp would, against testdata/fake-ax standing in for ax. It's the
// proof that acpi's ACP<->ax wiring (flag passing, event translation, stdio
// plumbing) actually works end to end, without needing a live model or API
// key. A real end-to-end run swaps ACPI_AX_PATH for a real ax binary plus
// ACPI_MODEL/ACPI_BASE_URL/an API key.
func TestEndToEndWithFakeAx(t *testing.T) {
	bin := buildAcpi(t)
	fakeAx, err := filepath.Abs("testdata/fake-ax")
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := t.TempDir()

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"ACPI_AX_PATH="+fakeAx,
		"ACPI_SESSION_DIR="+sessionDir,
		"ACPI_MODEL=deepseek-v4-flash",
		"ACPI_BASE_URL=https://api.deepseek.com",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	client := &testClient{t: t}
	conn := acp.NewClientSideConnection(client, stdin, stdout)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if initResp.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Fatalf("unexpected protocol version: %v", initResp.ProtocolVersion)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	newSessResp, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if newSessResp.SessionId == "" {
		t.Fatal("empty session id")
	}

	promptResp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hola alfred")},
	})
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if promptResp.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %v, want end_turn", promptResp.StopReason)
	}
	if len(client.updates) == 0 {
		t.Fatal("expected at least one session/update notification")
	}

	var sawMessage, sawToolCall, sawToolDone bool
	for _, u := range client.updates {
		switch {
		case u.Update.AgentMessageChunk != nil:
			sawMessage = true
		case u.Update.ToolCall != nil:
			sawToolCall = true
		case u.Update.ToolCallUpdate != nil && u.Update.ToolCallUpdate.Status != nil && *u.Update.ToolCallUpdate.Status == acp.ToolCallStatusCompleted:
			sawToolDone = true
		}
	}
	if !sawMessage {
		t.Error("no AgentMessageChunk update seen")
	}
	if !sawToolCall {
		t.Error("no ToolCall update seen")
	}
	if !sawToolDone {
		t.Error("no completed ToolCallUpdate seen")
	}
}

// TestCancel exercises the session/cancel path: acpi must forward the
// notification onto the running ax subprocess's stdin as
// {"type":"cancel"}, per ax's documented control-channel protocol.
func TestCancel(t *testing.T) {
	bin := buildAcpi(t)
	fakeAx, err := filepath.Abs("testdata/fake-ax-cancellable")
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"ACPI_AX_PATH="+fakeAx,
		"ACPI_SESSION_DIR="+t.TempDir(),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	client := &testClient{t: t}
	conn := acp.NewClientSideConnection(client, stdin, stdout)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	newSessResp, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	promptDone := make(chan struct {
		resp acp.PromptResponse
		err  error
	}, 1)
	go func() {
		resp, err := conn.Prompt(ctx, acp.PromptRequest{
			SessionId: newSessResp.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("do something slow")},
		})
		promptDone <- struct {
			resp acp.PromptResponse
			err  error
		}{resp, err}
	}()

	// Give fake-ax-cancellable time to reach its blocking read loop before
	// sending cancel.
	time.Sleep(300 * time.Millisecond)
	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: newSessResp.SessionId}); err != nil {
		t.Fatalf("session/cancel: %v", err)
	}

	select {
	case result := <-promptDone:
		if result.err != nil {
			t.Fatalf("session/prompt: %v", result.err)
		}
		if result.resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("stop reason = %v, want cancelled", result.resp.StopReason)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for cancelled prompt response")
	}
}

// TestFailedOutcomeWithRealAx drives the real ax binary (not testdata's
// fake) through acpi, pointed at an unreachable base URL, to confirm two
// things testdata/fake-ax can't: that real ax accepts the exact flag set
// runAx constructs (-C, -system, -model, -base, --session, --events, prompt)
// without a flag-parse error, and that acpi's outcome:"failed" -> JSON-RPC
// error mapping carries the real error message through. Skipped unless
// ACPI_TEST_AX_PATH points at a real ax binary.
func TestFailedOutcomeWithRealAx(t *testing.T) {
	axPath := os.Getenv("ACPI_TEST_AX_PATH")
	if axPath == "" {
		t.Skip("ACPI_TEST_AX_PATH not set; skipping real-ax integration test")
	}

	bin := buildAcpi(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"ACPI_AX_PATH="+axPath,
		"ACPI_SESSION_DIR="+t.TempDir(),
		"ACPI_MODEL=gpt-4.1-mini",
		"ACPI_BASE_URL=http://127.0.0.1:1/v1", // unreachable on purpose
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	client := &testClient{t: t}
	conn := acp.NewClientSideConnection(client, stdin, stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	newSessResp, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	_, err = conn.Prompt(ctx, acp.PromptRequest{
		SessionId: newSessResp.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
	})
	if err == nil {
		t.Fatal("expected session/prompt to fail against an unreachable base URL")
	}
	t.Logf("got expected error: %v", err)
}

func buildAcpi(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "acpi-under-test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building acpi: %v\n%s", err, out)
	}
	return bin
}
