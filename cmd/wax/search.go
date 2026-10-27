package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DeepSeek Search and Fetch are plain JSON APIs billed per request
// (https://api-docs.deepseek.com/guides/search). wax uses them for web_search;
// web_fetch keeps the local renderer because it handles JavaScript.
const (
	deepseekDefaultBase = "https://api.deepseek.com"
	maxSearchOutput     = 64 << 10
	defaultSearchCount  = 10
)

type searchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    string  `json:"site_name"`
	SiteIcon    string  `json:"site_icon"`
	Date        string  `json:"date"`
	Authority   int     `json:"authority"`
}

type searchRequest struct {
	Query       string `json:"query"`
	Count       int    `json:"count,omitempty"`
	NeedContent bool   `json:"need_content,omitempty"`
	OriQuestion string `json:"ori_question,omitempty"`
}

// deepseekKey is deliberately narrower than ax's key lookup: sending an
// OpenAI key to DeepSeek's search endpoint would just fail, so only a
// DeepSeek key enables the tool.
func deepseekKey() string { return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) }

func deepseekBase() string {
	base := strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
	if base == "" {
		base = deepseekDefaultBase
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/v1")
	return base
}

// searchEndpoint is the tools URL for the configured base.
func searchEndpoint(base string) string {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return deepseekDefaultBase + "/v1/tools/search"
	}
	path := strings.TrimRight(parsed.Path, "/")
	path = strings.TrimSuffix(path, "/v1")
	parsed.Path = path + "/v1/tools/search"
	parsed.RawQuery, parsed.Fragment = "", ""
	return parsed.String()
}

// searchWeb runs one DeepSeek Search request, retrying the statuses the docs
// call retryable (408, 429, 5xx).
func searchWeb(ctx context.Context, client *http.Client, req searchRequest) ([]searchResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("query is required")
	}
	if req.Count <= 0 {
		req.Count = defaultSearchCount
	}
	if req.Count > 50 {
		req.Count = 50
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	endpoint := searchEndpoint(deepseekBase())
	key := deepseekKey()
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusOK {
			var out struct {
				Results []searchResult `json:"results"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				return nil, fmt.Errorf("decode search response: %w", err)
			}
			return out.Results, nil
		}
		last = apiError(resp.StatusCode, body)
		if resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, last
		}
	}
	return nil, last
}

func apiError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		return fmt.Errorf("deepseek search: HTTP %d: %s", status, envelope.Error.Message)
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("deepseek search: HTTP %d: %s", status, message)
}

// formatSearch renders results for the model: title, link, provenance, the
// long summary and, when asked, a slice of the page content.
func formatSearch(results []searchResult) string {
	if len(results) == 0 {
		return "No results."
	}
	var out strings.Builder
	for i, result := range results {
		title := firstNonEmptyString(result.Title, result.URL)
		fmt.Fprintf(&out, "%d. [%s](%s)\n", i+1, title, result.URL)
		var meta []string
		if result.SiteName != "" {
			meta = append(meta, result.SiteName)
		}
		if len(result.Date) >= 10 {
			meta = append(meta, result.Date[:10])
		}
		if result.Authority >= 3 {
			meta = append(meta, "authoritative")
		}
		if len(meta) > 0 {
			fmt.Fprintf(&out, "   %s\n", strings.Join(meta, " · "))
		}
		text := result.Summary
		if text == "" {
			text = result.Snippet
		}
		if text != "" {
			fmt.Fprintf(&out, "   %s\n", strings.TrimSpace(text))
		}
		if content := strings.TrimSpace(result.Content); content != "" {
			fmt.Fprintf(&out, "   Content: %s\n", truncateString(content, 4000))
		}
		out.WriteString("\n")
		if out.Len() > maxSearchOutput {
			out.WriteString("(more results omitted)\n")
			break
		}
	}
	return strings.TrimSpace(out.String())
}

func truncateString(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
