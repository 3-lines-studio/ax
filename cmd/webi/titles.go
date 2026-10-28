package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// titlePrompt keeps the model on a short task: a title, nothing else. The
// conversation language wins, so a Spanish thread does not get an English name.
const titleSystem = "You name chat threads. Reply with the title only: no quotes, no markdown, no trailing period, at most six words, in the language of the conversation."

// generateTitle asks the model for a name and stores it. It runs after a run
// finishes, in the background: a thread that already has a derived title from
// its first message simply gets a better one, and a failure leaves that title
// in place.
func (s *Server) generateTitle(thread Thread, prompt, answer string) {
	key := titleKey()
	if key == "" {
		return
	}
	settings := s.loadSettings()
	base := strings.TrimRight(firstNonEmpty(settings.BaseURL, s.cfg.BaseURL, "https://api.deepseek.com"), "/")
	model := firstNonEmpty(settings.Model, s.cfg.Model, "deepseek-flash")

	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": titleSystem},
			{"role": "user", "content": "User: " + clip(prompt, 1500) + "\n\nAssistant: " + clip(answer, 1500)},
		},
		"max_tokens":  32,
		"temperature": 0.3,
		"stream":      false,
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		slog.Warn("webi: title request", "thread", thread.ID, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("webi: title request", "thread", thread.ID, "status", resp.StatusCode)
		return
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return
	}
	if len(parsed.Choices) == 0 {
		return
	}
	title := cleanTitle(parsed.Choices[0].Message.Content)
	if title == "" {
		return
	}
	if err := s.store.SetTitle(thread.ID, title); err != nil {
		slog.Warn("webi: store title", "thread", thread.ID, "error", err)
		return
	}
	s.publish(map[string]any{"type": "state"})
}

// cleanTitle makes a model answer safe to show: one line, no quoting, bounded.
func cleanTitle(raw string) string {
	title := strings.TrimSpace(raw)
	title = strings.TrimPrefix(title, "Title:")
	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'`«»“”")
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return ""
	}
	return truncate(title, 60)
}

func clip(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "…"
}

// titleKey resolves the key the same way ax does, so webi does not ask for the
// secret twice.
func titleKey() string {
	for _, name := range []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	if root := strings.TrimSpace(os.Getenv("BOT_ROOT")); root != "" {
		if data, err := os.ReadFile(filepath.Join(root, "secrets", "api-key")); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}
