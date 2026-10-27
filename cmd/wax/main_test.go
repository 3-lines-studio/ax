package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConvert(t *testing.T) {
	pageURL, err := url.Parse("https://example.com/docs/page")
	if err != nil {
		t.Fatal(err)
	}
	input := `<html><body><header><h2>Product Docs</h2></header><nav><a href="menu">Menu</a></nav><main><h1>Guide</h1><p>Useful text.</p><a href="next">Next</a><a href="">Empty</a><pre><code>go test ./...</code></pre></main><footer>Footer</footer></body></html>`

	output, textLength, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Product Docs", "# Guide", "Useful text.", "https://example.com/docs/next", "go test ./...", "## Links", "<https://example.com/docs/menu>"} {
		if !strings.Contains(output, want) {
			t.Errorf("output does not contain %q:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{"Menu", "Footer"} {
		if strings.Contains(output, unwanted) {
			t.Errorf("output contains %q:\n%s", unwanted, output)
		}
	}
	if strings.Contains(output, "[Empty](") {
		t.Errorf("empty link was not reduced to text:\n%s", output)
	}
	if textLength == 0 {
		t.Error("text length is zero")
	}
}

func TestConvertRejectsLargeMarkdown(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := "<body><p>" + strings.Repeat("x", maxMarkdown+1) + "</p></body>"

	_, _, err = convert(input, pageURL)
	if err == nil || err.Error() != "markdown exceeds 1 MiB" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchStatic(t *testing.T) {
	text := strings.Repeat("useful content ", 50)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if _, err := fmt.Fprintf(w, "<main><h1>Static</h1><p>%s</p></main>", text); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := fetch(ctx, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "# Static") || !strings.Contains(output, strings.TrimSpace(text)) {
		t.Fatalf("unexpected output:\n%s", output)
	}
}

func TestFetchStaticCharset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1252")
		if _, err := w.Write([]byte("<main><p>caf\xe9</p></main>")); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := fetchStatic(context.Background(), pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "café") {
		t.Fatalf("charset was not decoded: %q", body)
	}
}

func TestFetchRendered(t *testing.T) {
	if os.Getenv("WAX_SKIP_BROWSER_TEST") == "1" {
		t.Skip("browser test disabled")
	}
	if _, err := chromiumPath(); err != nil {
		t.Skip(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if _, err := fmt.Fprint(w, `<html><body><div id="app"></div><script>document.getElementById("app").innerHTML = "<main><h1>Rendered</h1><p>Loaded by JavaScript.</p></main>"</script></body></html>`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	output, err := fetch(ctx, pageURL)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "# Rendered") || !strings.Contains(output, "Loaded by JavaScript.") {
		t.Fatalf("unexpected output:\n%s", output)
	}

	missingURL, err := url.Parse(server.URL + "/missing")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	_, _, err = fetchRendered(ctx, missingURL)
	cancel()
	if err == nil || err.Error() != "HTTP 404" {
		t.Fatalf("unexpected rendered HTTP error: %v", err)
	}
}
