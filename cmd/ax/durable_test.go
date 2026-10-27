package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOneShotRepairsInterruptedSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.jsonl")
	log, err := openSessionLog(path)
	if err != nil {
		t.Fatal(err)
	}
	user := Message{Role: "user", Content: "run the tool"}
	call := ToolCall{ID: "c1", Name: "bash", Arguments: "{}"}
	assistant := Message{Role: "assistant", ToolCalls: []ToolCall{call}}
	entries := []sessionEntry{
		{Type: entryRunStart, RunID: "old", Prompt: "run the tool", Model: "test-model"},
		{Type: entryMessage, RunID: "old", Message: &user},
		{Type: entryMessage, RunID: "old", Message: &assistant},
		{Type: entryToolIntent, RunID: "old", Call: &call},
	}
	for _, entry := range entries {
		if err := log.append(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &request)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("AX_TOOLS", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", "")

	devnull, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devnull.Close() }()
	oldStdin := os.Stdin
	os.Stdin = devnull
	defer func() { os.Stdin = oldStdin }()

	cfg := cliConfig{base: srv.URL, model: "test-model", session: path}
	bot := resolveBotCtx(dir)
	_ = captureStdout(t, func() {
		oneShotEvents(cfg, fileConfig{}, bot, []string{"continue"})
	})

	messages, ok := request["messages"].([]any)
	if !ok {
		t.Fatalf("request = %v", request)
	}
	var roles []string
	for _, raw := range messages {
		roles = append(roles, raw.(map[string]any)["role"].(string))
	}
	want := []string{"system", "user", "assistant", "tool", "user"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v", roles, want)
		}
	}
	toolMessage := messages[3].(map[string]any)
	if !strings.Contains(toolMessage["content"].(string), "interrupted") {
		t.Errorf("tool result = %v", toolMessage["content"])
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(after), string(before)) {
		t.Error("session log was rewritten instead of appended")
	}
	loaded, err := loadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded[len(loaded)-1].Type != entryRunEnd {
		t.Errorf("last entry = %+v", loaded[len(loaded)-1])
	}
}

func TestOneShotPersistsImagesAsBlobs(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "run.jsonl")
	pngPath := writeTempPNG(t, dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("AX_TOOLS", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", "")

	devnull, _ := os.Open("/dev/null")
	defer func() { _ = devnull.Close() }()
	oldStdin := os.Stdin
	os.Stdin = devnull
	defer func() { os.Stdin = oldStdin }()

	cfg := cliConfig{base: srv.URL, model: "test-model", session: session, images: []string{pngPath}, imageDetail: "low"}
	_ = captureStdout(t, func() {
		oneShotEvents(cfg, fileConfig{}, resolveBotCtx(dir), []string{"look"})
	})

	entries, err := loadPath(session)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	blob := ""
	for _, entry := range entries {
		if entry.Type == entryMessage && entry.Message != nil && entry.Message.HasImages() {
			found = true
			image := entry.Message.ContentParts[1].Image
			if image == nil || image.Blob == "" || image.Detail != "low" {
				t.Errorf("image part = %+v", image)
			}
			blob = image.Blob
			break
		}
	}
	if !found {
		t.Fatal("session did not record the image part")
	}
	if _, err := os.Stat(filepath.Join(blobDir(session), blob)); err != nil {
		t.Errorf("blob missing: %v", err)
	}
}
