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
	"time"
)

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		`"Fix the login bug"`:     "Fix the login bug",
		"Title: Deploy webi\n":    "Deploy webi",
		"  `refactor   parser`  ": "refactor parser",
		"«Título en español»":     "Título en español",
		"":                        "",
		"a\nb":                    "a b",
	}
	for input, want := range cases {
		if got := cleanTitle(input); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", input, got, want)
		}
	}
	if got := cleanTitle("\""); got != "" {
		t.Errorf("a bare quote is not a title: %q", got)
	}
	long := strings.Repeat("word ", 30)
	if got := cleanTitle(long); !strings.HasSuffix(got, "…") || len([]rune(got)) != 61 {
		t.Errorf("long title = %q (%d runes)", got, len([]rune(got)))
	}
}

func TestGenerateTitleStoresTheModelAnswer(t *testing.T) {
	var seen map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &seen)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"\"Ship the ssh backend\""}}]}`)
	}))
	defer api.Close()

	srv, channel := newTestServer(t, fakeAXPath(t))
	srv.cfg.BaseURL = api.URL
	srv.cfg.Model = "deepseek-flash"
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	t.Setenv("BOT_ROOT", "")

	thread, err := srv.store.CreateThread(channel, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.generateTitle(thread, "move the fs to ssh", "Done: fsx now runs over sshx.")

	stored, err := srv.store.Thread(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Ship the ssh backend" {
		t.Fatalf("title = %q", stored.Title)
	}
	if !stored.TitleGenerated {
		t.Fatal("title was not marked generated")
	}
	if seen["model"] != "deepseek-flash" || seen["stream"] != false {
		t.Fatalf("request = %+v", seen)
	}
	srv.generateTitle(thread, "another prompt", "another answer")
	again, _ := srv.store.Thread(thread.ID)
	if again.Title != "Ship the ssh backend" {
		t.Fatalf("title changed to %q", again.Title)
	}
}

func TestGenerateTitleWithoutAKeyIsANoOp(t *testing.T) {
	srv, channel := newTestServer(t, fakeAXPath(t))
	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a key")
	}))
	defer failure.Close()
	srv.cfg.BaseURL = failure.URL
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("BOT_ROOT", "")
	thread, err := srv.store.CreateThread(channel, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.generateTitle(thread, "hello", "hi")
	stored, _ := srv.store.Thread(thread.ID)
	if stored.TitleGenerated {
		t.Fatalf("title = %+v", stored)
	}
}

func TestTitleKeyFallsBackToTheBotSecret(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secrets", "api-key"), []byte("sk-bot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("BOT_ROOT", root)
	if got := titleKey(); got != "sk-bot" {
		t.Fatalf("key = %q", got)
	}
}

func fakeAXPath(t *testing.T) string {
	t.Helper()
	path, _ := fakeAX(t)
	return path
}

var _ = time.Second
