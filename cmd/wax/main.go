package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	markdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const maxHTML = 10 << 20
const maxMarkdown = 1 << 20
const usefulTextLength = 500

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 1 && args[0] == "describe" {
		fmt.Println(`{"name":"web_fetch","description":"Fetch a URL as Markdown with automatic Chromium rendering","parameters":{"type":"object","properties":{"url":{"type":"string","description":"HTTP or HTTPS URL"}},"required":["url"]},"snippet":"Fetch URL as Markdown with Wax"}`)
		return
	}
	if len(args) == 2 && args[0] == "run" && args[1] == "web_fetch" {
		var input struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&input); err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid arguments: %v\n", err)
			os.Exit(2)
		}
		run(input.URL)
		return
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: wax [--] URL")
		os.Exit(2)
	}
	run(args[0])
}

func run(rawURL string) {
	pageURL, err := url.ParseRequestURI(rawURL)
	if err != nil || (pageURL.Scheme != "http" && pageURL.Scheme != "https") || pageURL.Host == "" {
		fmt.Fprintln(os.Stderr, "error: URL must use http or https")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := fetch(ctx, pageURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(output)
}

func fetch(ctx context.Context, pageURL *url.URL) (string, error) {
	body, finalURL, staticErr := fetchStatic(ctx, pageURL)
	if staticErr == nil {
		output, textLength, err := convert(body, finalURL)
		if err == nil && textLength >= usefulTextLength {
			return output, nil
		}
	}

	body, finalURL, err := fetchRendered(ctx, pageURL)
	if err != nil {
		if staticErr != nil {
			return "", fmt.Errorf("static fetch: %v; rendered fetch: %w", staticErr, err)
		}
		return "", fmt.Errorf("rendered fetch: %w", err)
	}
	output, _, err := convert(body, finalURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(output) == "" {
		return "", errors.New("page has no readable content")
	}
	return output, nil
}

func fetchStatic(ctx context.Context, pageURL *url.URL) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; wax/0.1)")

	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		_ = res.Body.Close()
	}()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", nil, fmt.Errorf("HTTP %s", res.Status)
	}
	contentType := res.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "text/html") {
		return "", nil, fmt.Errorf("unsupported content type %s", contentType)
	}

	decoded, err := charset.NewReader(io.LimitReader(res.Body, maxHTML+1), contentType)
	if err != nil {
		return "", nil, err
	}
	data, err := io.ReadAll(io.LimitReader(decoded, maxHTML+1))
	if err != nil {
		return "", nil, err
	}
	if len(data) > maxHTML {
		return "", nil, errors.New("page exceeds 10 MiB")
	}
	return string(data), res.Request.URL, nil
}

func fetchRendered(ctx context.Context, pageURL *url.URL) (string, *url.URL, error) {
	chromium, err := chromiumPath()
	if err != nil {
		return "", nil, err
	}

	browserLauncher := launcher.New().Context(ctx).Bin(chromium).Headless(true)
	if os.Getenv("WAX_NO_SANDBOX") == "1" {
		browserLauncher.NoSandbox(true)
	}
	controlURL, err := browserLauncher.Launch()
	if err != nil {
		return "", nil, err
	}
	browser := rod.New().Context(ctx).ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return "", nil, err
	}
	defer func() {
		_ = browser.Close()
	}()

	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return "", nil, err
	}
	defer func() {
		_ = page.Close()
	}()
	if err := page.Navigate(pageURL.String()); err != nil {
		return "", nil, err
	}
	if err := page.WaitLoad(); err != nil && ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	statusResult, err := page.Eval("() => performance.getEntriesByType('navigation')[0]?.responseStatus || 0")
	if err != nil {
		return "", nil, err
	}
	status := statusResult.Value.Int()
	if status != 0 && (status < 200 || status >= 300) {
		return "", nil, fmt.Errorf("HTTP %d", status)
	}
	waitForStableText(ctx, page)

	body, err := page.HTML()
	if err != nil {
		return "", nil, err
	}
	info, err := page.Info()
	if err != nil {
		return "", nil, err
	}
	finalURL, err := url.Parse(info.URL)
	if err != nil {
		return "", nil, err
	}
	return body, finalURL, nil
}

func chromiumPath() (string, error) {
	for _, name := range []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("chromium is not installed")
}

func waitForStableText(ctx context.Context, page *rod.Page) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	last := ""
	stable := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			body, err := page.Element("body")
			if err != nil {
				continue
			}
			text, err := body.Text()
			if err != nil {
				continue
			}
			if text == last && strings.TrimSpace(text) != "" {
				stable++
				if stable == 4 {
					return
				}
				continue
			}
			last = text
			stable = 0
		}
	}
}

func convert(input string, pageURL *url.URL) (string, int, error) {
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", 0, err
	}
	normalizeURLs(doc, pageURL)
	links := collectLinks(doc)
	clean(doc)
	content := findContent(doc)
	textLength := len([]rune(visibleText(content)))

	var htmlOutput strings.Builder
	if err := html.Render(&htmlOutput, content); err != nil {
		return "", 0, err
	}
	output, err := markdown.ConvertString(htmlOutput.String())
	if err != nil {
		return "", 0, err
	}
	output = strings.TrimSpace(output)
	if len(links) > 0 {
		output += "\n\n## Links\n"
		for _, link := range links {
			output += "\n- <" + link + ">"
		}
	}
	if len(output) > maxMarkdown {
		return "", 0, errors.New("markdown exceeds 1 MiB")
	}
	return output, textLength, nil
}

func clean(node *html.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		if removeNode(child) {
			node.RemoveChild(child)
		} else {
			clean(child)
		}
		child = next
	}
}

func removeNode(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}
	switch node.Data {
	case "script", "style", "noscript", "svg", "canvas", "nav", "footer", "form", "dialog", "template":
		return true
	}
	for _, attr := range node.Attr {
		if attr.Key == "hidden" || attr.Key == "aria-hidden" && attr.Val == "true" {
			return true
		}
	}
	return false
}

func findContent(node *html.Node) *html.Node {
	if found := findElement(node, func(n *html.Node) bool { return n.Data == "body" }); found != nil {
		return found
	}
	return node
}

func findElement(node *html.Node, match func(*html.Node) bool) *html.Node {
	if node.Type == html.ElementNode && match(node) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, match); found != nil {
			return found
		}
	}
	return nil
}

func normalizeURLs(node *html.Node, pageURL *url.URL) {
	if node.Type == html.ElementNode {
		attrs := node.Attr[:0]
		hasHref := false
		for _, attr := range node.Attr {
			if attr.Key == "href" && strings.TrimSpace(attr.Val) == "" {
				continue
			}
			if attr.Key == "href" || attr.Key == "src" {
				parsed, err := url.Parse(attr.Val)
				if err == nil {
					attr.Val = pageURL.ResolveReference(parsed).String()
				}
			}
			if attr.Key == "href" {
				hasHref = true
			}
			attrs = append(attrs, attr)
		}
		node.Attr = attrs
		if node.Data == "a" && !hasHref {
			node.Data = "span"
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		normalizeURLs(child, pageURL)
	}
}

func collectLinks(node *html.Node) []string {
	links := make([]string, 0)
	seen := make(map[string]bool)
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && current.Data == "a" {
			for _, attr := range current.Attr {
				if attr.Key != "href" {
					continue
				}
				parsed, err := url.Parse(attr.Val)
				if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || seen[attr.Val] {
					continue
				}
				seen[attr.Val] = true
				links = append(links, attr.Val)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return links
}

func visibleText(node *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
			text.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(text.String()), " ")
}
