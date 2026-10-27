package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStdioClientWithSDKServer(t *testing.T) {
	bin := buildFakeServer(t)
	client, err := New(ServerConfig{Name: "fake", Command: bin, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if client.Protocol() != LatestProtocol {
		t.Errorf("protocol = %q", client.Protocol())
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name == "" || tools[0].InputSchema == nil {
		t.Fatalf("tools = %+v", tools)
	}
	result, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text(), "echo: hi") {
		t.Errorf("result = %+v text=%q", result, result.Text())
	}
	result, err = client.CallTool(ctx, "fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Text(), "deliberately failed") {
		t.Errorf("fail result = %+v", result)
	}
}

func buildFakeServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-server")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fake-server")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake server: %v\n%s", err, out)
	}
	return bin
}

type rpcCall struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Meta map[string]any  `json:"_meta"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments"`
	} `json:"params"`
}

func TestHTTPClientJSONAndSSE(t *testing.T) {
	var calls int
	var sawMeta, sawVersionHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req rpcCall
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Params.Meta["io.modelcontextprotocol/protocolVersion"] == LatestProtocol {
			sawMeta = true
		}
		if r.Header.Get("Mcp-Protocol-Version") == LatestProtocol {
			sawVersionHeader = true
		}
		switch req.Method {
		case "server/discover":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"supportedVersions": []string{LatestProtocol},
				"capabilities":      map[string]any{"tools": map[string]any{}},
				"serverInfo":        map[string]any{"name": "http-fake", "version": "1"},
				"ttlMs":             1000,
			}})
		case "tools/list":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []any{
					map[string]any{"name": "echo", "description": "Echo", "inputSchema": map[string]any{"type": "object"}},
				},
			}})
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "hello from sse"}},
			}}))
		default:
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "no"}})
		}
	}))
	defer srv.Close()

	client, err := New(ServerConfig{Name: "http", URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if !sawMeta {
		t.Error("discover request did not carry _meta")
	}
	if client.TTL() != time.Second {
		t.Errorf("ttl = %s", client.TTL())
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Errorf("tools = %+v", tools)
	}
	if !sawVersionHeader {
		t.Error("tools/list did not carry Mcp-Protocol-Version")
	}
	result, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Text(), "hello from sse") {
		t.Errorf("result = %+v", result)
	}
	if calls < 3 {
		t.Errorf("calls = %d", calls)
	}
}

func TestHTTPClientFallsBackToInitialize(t *testing.T) {
	var sawSession bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcCall
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch req.Method {
		case "server/discover":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "unknown method"}})
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "legacy", "version": "1"},
			}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sawSession = r.Header.Get("Mcp-Session-Id") == "sess-1"
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{}}})
		default:
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "no"}})
		}
	}))
	defer srv.Close()

	client, err := New(ServerConfig{Name: "legacy", URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if client.Protocol() != "2025-11-25" {
		t.Errorf("protocol = %q", client.Protocol())
	}
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawSession {
		t.Error("session id from initialize was not sent on tools/list")
	}
}

func TestHTTPClientPaginatesTools(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcCall
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "server/discover":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"supportedVersions": []string{LatestProtocol}, "serverInfo": map[string]any{"name": "p", "version": "1"},
			}})
		case "tools/list":
			page++
			result := map[string]any{"tools": []any{map[string]any{"name": fmt.Sprintf("t%d", page), "inputSchema": map[string]any{"type": "object"}}}}
			if page == 1 {
				result["nextCursor"] = "next"
			}
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
	}))
	defer srv.Close()
	client, err := New(ServerConfig{Name: "p", URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "t1" || tools[1].Name != "t2" {
		t.Errorf("tools = %+v", tools)
	}
}

func TestResultTextFlattensContent(t *testing.T) {
	result := Result{Content: []Content{
		{Type: "text", Text: "hello"},
		{Type: "image", MimeType: "image/png", Data: "AAAA"},
		{Type: "resource_link", URI: "file:///x", Name: "x"},
	}}
	text := result.Text()
	for _, want := range []string{"hello", "image image/png", "file:///x"} {
		if !strings.Contains(text, want) {
			t.Errorf("text = %q, missing %q", text, want)
		}
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(mustJSON(value)))
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
