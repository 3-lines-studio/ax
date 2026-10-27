package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func setFakeHTTP(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(fn)}
	t.Cleanup(func() { http.DefaultClient = old })
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

type capturedCall struct {
	path string
	form url.Values
}

func TestOpenSocketSuccess(t *testing.T) {
	var captured *http.Request
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		captured = r.Clone(context.Background())
		return jsonResponse(`{"ok":true,"url":"wss://socket.example"}`), nil
	})
	cfg := config{appToken: "app-token"}
	sockURL, err := openSocket(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sockURL != "wss://socket.example" {
		t.Fatalf("url %q", sockURL)
	}
	if captured.Method != http.MethodPost {
		t.Fatalf("method %q", captured.Method)
	}
	if captured.URL.String() != "https://slack.com/api/apps.connections.open" {
		t.Fatalf("url %q", captured.URL.String())
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer app-token" {
		t.Fatalf("authorization %q", got)
	}
}

func TestOpenSocketError(t *testing.T) {
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"ok":false,"error":"rate_limited"}`), nil
	})
	_, err := openSocket(context.Background(), config{appToken: "x"})
	if err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Fatalf("err %v", err)
	}
}

func TestOpenSocketBadJSON(t *testing.T) {
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`not-json`), nil
	})
	_, err := openSocket(context.Background(), config{appToken: "x"})
	if err == nil || !strings.Contains(err.Error(), "decode socket response") {
		t.Fatalf("err %v", err)
	}
}

func TestOpenSocketNetworkError(t *testing.T) {
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})
	_, err := openSocket(context.Background(), config{appToken: "x"})
	if err == nil || !strings.Contains(err.Error(), "open socket") {
		t.Fatalf("err %v", err)
	}
}

func TestSlackAPISuccess(t *testing.T) {
	var captured *http.Request
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		captured = r.Clone(context.Background())
		return jsonResponse(`{"ok":true,"ts":"1.99"}`), nil
	})
	res, err := slackAPI(config{botToken: "bot-token"}, "chat.postMessage", url.Values{"channel": {"C1"}, "text": {"hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.TS != "1.99" {
		t.Fatalf("res %#v", res)
	}
	if captured.Method != http.MethodPost || captured.URL.Path != "/api/chat.postMessage" {
		t.Fatalf("method %q path %q", captured.Method, captured.URL.Path)
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer bot-token" {
		t.Fatalf("auth %q", got)
	}
	_ = captured.ParseForm()
	if captured.PostForm.Get("channel") != "C1" || captured.PostForm.Get("text") != "hi" {
		t.Fatalf("form %v", captured.PostForm)
	}
}

func TestSlackAPIReturnsError(t *testing.T) {
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"ok":false,"error":"invalid_auth"}`), nil
	})
	_, err := slackAPI(config{botToken: "x"}, "chat.postMessage", url.Values{})
	if err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Fatalf("err %v", err)
	}
}

func TestSlackAPIBadJSON(t *testing.T) {
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`not-json`), nil
	})
	_, err := slackAPI(config{botToken: "x"}, "chat.postMessage", url.Values{})
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestPostMessage(t *testing.T) {
	var captured *http.Request
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		captured = r.Clone(context.Background())
		return jsonResponse(`{"ok":true,"ts":"1.99"}`), nil
	})
	ts, err := postMessage(config{botToken: "bot-token"}, job{channel: "C1", threadTS: "1.0"}, "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if ts != "1.99" {
		t.Fatalf("ts %q", ts)
	}
	if captured.URL.Path != "/api/chat.postMessage" {
		t.Fatalf("path %q", captured.URL.Path)
	}
	_ = captured.ParseForm()
	if captured.PostForm.Get("channel") != "C1" || captured.PostForm.Get("thread_ts") != "1.0" || captured.PostForm.Get("text") != "hello world" {
		t.Fatalf("form %v", captured.PostForm)
	}
}

func TestUpdateMessage(t *testing.T) {
	var captured *http.Request
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		captured = r.Clone(context.Background())
		return jsonResponse(`{"ok":true}`), nil
	})
	if err := updateMessage(config{botToken: "bot-token"}, "C1", "1.0", "new text"); err != nil {
		t.Fatal(err)
	}
	if captured.URL.Path != "/api/chat.update" {
		t.Fatalf("path %q", captured.URL.Path)
	}
	_ = captured.ParseForm()
	if captured.PostForm.Get("channel") != "C1" || captured.PostForm.Get("ts") != "1.0" || captured.PostForm.Get("text") != "new text" {
		t.Fatalf("form %v", captured.PostForm)
	}
}

func TestServeOpenSocketError(t *testing.T) {
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"ok":false,"error":"no_socket"}`), nil
	})
	err := serve(context.Background(), config{appToken: "x"}, make(chan job), make(map[string]struct{}))
	if err == nil || !strings.Contains(err.Error(), "no_socket") {
		t.Fatalf("err %v", err)
	}
}

func TestServeDialError(t *testing.T) {
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"ok":true,"url":"ws://127.0.0.1:1/socket"}`), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := serve(ctx, config{appToken: "x"}, make(chan job), make(map[string]struct{}))
	if err == nil || !strings.Contains(err.Error(), "connect socket") {
		t.Fatalf("err %v", err)
	}
}

func TestServeProcessesEvents(t *testing.T) {
	mention := json.RawMessage(`{"team_id":"T1","event":{"type":"app_mention","channel":"C1","text":"<@U1> hi","ts":"1.0"},"authorizations":[{"user_id":"U1"}]}`)
	dm := json.RawMessage(`{"team_id":"T1","event":{"type":"message","channel":"D1","channel_type":"im","text":"hi there","ts":"1.5"}}`)
	envelopes := []string{
		`{"type":"events_api","payload":` + string(mention) + `}`,
		`{"envelope_id":"e1","type":"events_api","payload":` + string(mention) + `}`,
		`{"envelope_id":"e2","type":"events_api","payload":` + string(dm) + `}`,
		`{"envelope_id":"n1","type":"hello","payload":` + string(mention) + `}`,
		`{"envelope_id":"e1","type":"events_api","payload":` + string(mention) + `}`,
	}
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			for {
				if _, _, err := conn.Read(context.Background()); err != nil {
					return
				}
			}
		}()
		for _, env := range envelopes {
			if err := conn.Write(context.Background(), websocket.MessageText, []byte(env)); err != nil {
				return
			}
		}
		<-stop
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(func() {
		close(stop)
		server.Close()
	})
	serverURL := "ws" + strings.TrimPrefix(server.URL, "http")
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Upgrade") == "websocket" {
			return http.DefaultTransport.RoundTrip(r)
		}
		return jsonResponse(`{"ok":true,"url":"` + serverURL + `"}`), nil
	})

	jobs := make(chan job, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, config{appToken: "x", mentionOnly: true}, jobs, make(map[string]struct{}))
	}()
	select {
	case j := <-jobs:
		if j.teamID != "T1" || j.channel != "C1" || j.threadTS != "1.0" || j.prompt != "hi" {
			t.Fatalf("unexpected job %#v", j)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no job dispatched; serve returned: %v", <-done)
	}
	select {
	case j := <-jobs:
		t.Fatalf("unexpected second job %#v", j)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestWorkPostsAndUpdates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":\"done\"}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botToken: "bot", stateAX: filepath.Join(dir, "sessions"), artifacts: filepath.Join(dir, "artifacts")}
	var calls []capturedCall
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		calls = append(calls, capturedCall{path: r.URL.Path, form: r.PostForm})
		return jsonResponse(`{"ok":true,"ts":"1.0"}`), nil
	})
	jobs := make(chan job, 1)
	jobs <- job{channel: "C1", threadTS: "1.0"}
	close(jobs)
	work(cfg, jobs)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].path != "/api/chat.postMessage" || calls[0].form.Get("text") != "Working…" {
		t.Fatalf("call0 %#v", calls[0])
	}
	if calls[1].path != "/api/chat.update" || calls[1].form.Get("text") != "done" {
		t.Fatalf("call1 %#v", calls[1])
	}
}

func TestWorkPostsWithoutUpdateWhenNoProgressTS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"messages\":[{\"Role\":\"assistant\",\"Content\":\"done\"}]}' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	if err := os.WriteFile(ax, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botToken: "bot", stateAX: filepath.Join(dir, "sessions"), artifacts: filepath.Join(dir, "artifacts")}
	var calls []capturedCall
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		calls = append(calls, capturedCall{path: r.URL.Path, form: r.PostForm})
		return jsonResponse(`{"ok":true,"ts":""}`), nil
	})
	jobs := make(chan job, 1)
	jobs <- job{channel: "C1", threadTS: "1.0"}
	close(jobs)
	work(cfg, jobs)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].path != "/api/chat.postMessage" || calls[0].form.Get("text") != "Working…" {
		t.Fatalf("call0 %#v", calls[0])
	}
	if calls[1].path != "/api/chat.postMessage" || calls[1].form.Get("text") != "done" {
		t.Fatalf("call1 %#v", calls[1])
	}
}

func TestWorkPostsAXFailureReply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ax := filepath.Join(dir, "ax")
	if err := os.WriteFile(ax, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{axPath: ax, botToken: "bot", stateAX: filepath.Join(dir, "sessions"), artifacts: filepath.Join(dir, "artifacts")}
	var calls []capturedCall
	setFakeHTTP(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		calls = append(calls, capturedCall{path: r.URL.Path, form: r.PostForm})
		return jsonResponse(`{"ok":true,"ts":"1.0"}`), nil
	})
	jobs := make(chan job, 1)
	jobs <- job{channel: "C1", threadTS: "1.0"}
	close(jobs)
	work(cfg, jobs)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].path != "/api/chat.postMessage" || calls[0].form.Get("text") != "Working…" {
		t.Fatalf("call0 %#v", calls[0])
	}
	if calls[1].path != "/api/chat.update" || !strings.Contains(calls[1].form.Get("text"), "AX failed") {
		t.Fatalf("call1 %#v", calls[1])
	}
}
