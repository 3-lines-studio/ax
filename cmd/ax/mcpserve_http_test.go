package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/mcp"
)

func TestMCPHTTPHandlerAnswersJSONRPC(t *testing.T) {
	tools := catalogTools(testCatalog(t))
	srv := httptest.NewServer(mcpHTTPHandler(tools))
	defer srv.Close()

	post := func(body string) *http.Response {
		t.Helper()
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("initialize = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var init struct {
		Result struct {
			ServerInfo struct{ Name string } `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&init); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if init.Result.ServerInfo.Name != "ax" {
		t.Fatalf("initialize = %+v", init)
	}

	resp = post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var listed struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "search" || listed.Result.Tools[1].Name != "execute" {
		t.Fatalf("tools = %+v", listed.Result.Tools)
	}

	resp = post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification = %d, want 202", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET = %d allow=%q", resp.StatusCode, resp.Header.Get("Allow"))
	}

	req, err := http.NewRequest("POST", srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://evil.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin = %d, want 403", resp.StatusCode)
	}
}

func TestMCPHTTPClientRoundTrip(t *testing.T) {
	srv := httptest.NewServer(mcpHTTPHandler(catalogTools(testCatalog(t))))
	defer srv.Close()

	client, err := mcp.New(mcp.ServerConfig{Name: "ax", URL: srv.URL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	tools, err := client.ListTools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools = %+v err=%v", tools, err)
	}
	result, err := client.CallTool(ctx, "search", json.RawMessage(`{"query":"echo"}`))
	if err != nil || !strings.Contains(result.Text(), "fake.echo") {
		t.Fatalf("search = %+v err=%v", result, err)
	}
	result, err = client.CallTool(ctx, "execute", json.RawMessage(`{"tool":"fake.echo","arguments":{"message":"hi"}}`))
	if err != nil || result.IsError || !strings.Contains(result.Text(), `echo:{"message":"hi"}`) {
		t.Fatalf("execute = %+v err=%v", result, err)
	}
}
