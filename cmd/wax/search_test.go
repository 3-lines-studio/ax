package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearchEndpointNormalizesBase(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":    "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/":   "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1": "https://api.deepseek.com/v1/tools/search",
		"https://proxy.example/llm":   "https://proxy.example/llm/v1/tools/search",
	}
	for base, want := range cases {
		if got := searchEndpoint(base); got != want {
			t.Errorf("searchEndpoint(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestSearchWebSendsTheDocumentedRequest(t *testing.T) {
	var body map[string]any
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"rerank_score":2.4,"title":"DeepSeek V4","url":"https://example.com/v4","snippet":"V4 is out","summary":"DeepSeek released V4 with better tools.","content":"Long page text","site_name":"Example News","date":"2026-06-01T00:00:00+00:00","authority":3},{"title":"Second","url":"https://example.com/2","summary":"Another result"}]}`)
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL+"/v1")

	results, err := searchWeb(context.Background(), nil, searchRequest{Query: "DeepSeek V4 release", Count: 2, NeedContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/tools/search" {
		t.Errorf("path = %q", path)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("auth = %q", auth)
	}
	if body["query"] != "DeepSeek V4 release" || body["count"].(float64) != 2 || body["need_content"] != true {
		t.Errorf("body = %v", body)
	}
	if len(results) != 2 || results[0].Title != "DeepSeek V4" {
		t.Fatalf("results = %+v", results)
	}
	text := formatSearch(results)
	for _, want := range []string{"[DeepSeek V4](https://example.com/v4)", "Example News", "2026-06-01", "authoritative", "Long page text", "Another result"} {
		if !strings.Contains(text, want) {
			t.Errorf("formatSearch missing %q:\n%s", want, text)
		}
	}
}

func TestSearchWebRetriesRateLimits(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"ok","url":"https://example.com"}]}`)
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)
	results, err := searchWeb(context.Background(), nil, searchRequest{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("results=%+v calls=%d", results, calls)
	}
}

func TestSearchWebSurfacesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"'count' must be between 1 and 50"}}`)
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)
	_, err := searchWeb(context.Background(), nil, searchRequest{Query: "x", Count: 99})
	if err == nil || !strings.Contains(err.Error(), "'count' must be between 1 and 50") {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchWebRequiresQuery(t *testing.T) {
	if _, err := searchWeb(context.Background(), nil, searchRequest{}); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestFormatEmptyResults(t *testing.T) {
	if got := formatSearch(nil); got != "No results." {
		t.Errorf("got %q", got)
	}
}

func TestDescribeNeedsTheKey(t *testing.T) {
	isolate(t)
	var without bytes.Buffer
	describe(&without)
	if strings.Contains(without.String(), "web_search") {
		t.Errorf("web_search described without a key:\n%s", without.String())
	}
	if !strings.Contains(without.String(), "web_fetch") {
		t.Errorf("web_fetch missing:\n%s", without.String())
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	var with bytes.Buffer
	describe(&with)
	if !strings.Contains(with.String(), "web_search") {
		t.Errorf("web_search missing with a key:\n%s", with.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(with.String()), "\n") {
		var descriptor map[string]any
		if err := json.Unmarshal([]byte(line), &descriptor); err != nil {
			t.Fatalf("descriptor is not JSON: %v (%s)", err, line)
		}
		if descriptor["name"] == "" || descriptor["read_only"] != true {
			t.Errorf("descriptor = %v", descriptor)
		}
	}
}

func TestSearchCLIUsesTheEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"title":"CLI result","url":"https://example.com/cli","summary":"found it"}]}`)
	}))
	defer srv.Close()
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	t.Setenv("DEEPSEEK_BASE_URL", srv.URL)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	searchCLI([]string{"deepseek", "search", "-count", "3"})
	_ = w.Close()
	os.Stdout = old
	data, _ := io.ReadAll(r)
	if !strings.Contains(string(data), "[CLI result](https://example.com/cli)") {
		t.Fatalf("output = %q", data)
	}
}
