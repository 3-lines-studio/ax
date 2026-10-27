package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const maxOutput = 20000

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type arguments struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

type link struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type browserTarget struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 1 && args[0] == "describe" {
		printTools()
		return
	}
	if len(args) != 2 || args[0] != "run" {
		fail(2, "usage: browserx describe | browserx run TOOL")
	}
	tools := map[string]struct{}{
		"browser_open":       {},
		"browser_read":       {},
		"browser_links":      {},
		"browser_screenshot": {},
	}
	if _, ok := tools[args[1]]; !ok {
		fail(2, "unknown tool: "+args[1])
	}
	var input arguments
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&input); err != nil {
		fail(2, "invalid arguments: "+err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx, err := browserContext(ctx)
	if err != nil {
		fail(1, err.Error())
	}
	var result string
	switch args[1] {
	case "browser_open":
		result, err = openPage(ctx, input.URL)
	case "browser_read":
		result, err = readPage(ctx)
	case "browser_links":
		result, err = readLinks(ctx)
	case "browser_screenshot":
		result, err = screenshot(ctx, input.Name)
	}
	if err != nil {
		fail(1, err.Error())
	}
	fmt.Println(result)
}

func printTools() {
	fmt.Println(`{"name":"browser_open","description":"Open an HTTP or HTTPS URL in the visible browser","parameters":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}}`)
	fmt.Println(`{"name":"browser_read","description":"Read the title, URL, and visible text from the current browser page","parameters":{"type":"object","properties":{}}}`)
	fmt.Println(`{"name":"browser_links","description":"List visible links from the current browser page","parameters":{"type":"object","properties":{}}}`)
	fmt.Println(`{"name":"browser_screenshot","description":"Save a screenshot of the current browser page","parameters":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`)
}

func browserContext(parent context.Context) (context.Context, error) {
	endpoint := os.Getenv("BROWSERX_CDP_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:9222"
	}
	targetID, err := currentTarget(parent, endpoint)
	if err != nil {
		return nil, err
	}
	allocator, _ := chromedp.NewRemoteAllocator(parent, endpoint)
	ctx, _ := chromedp.NewContext(allocator, chromedp.WithTargetID(target.ID(targetID)))
	if err := chromedp.Run(ctx); err != nil {
		return nil, fmt.Errorf("connect to browser: %w", err)
	}
	return ctx, nil
}

func currentTarget(ctx context.Context, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/json/list", nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("list browser pages: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("list browser pages: %s", response.Status)
	}
	var targets []browserTarget
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&targets); err != nil {
		return "", fmt.Errorf("list browser pages: %w", err)
	}
	for _, target := range targets {
		if target.Type == "page" {
			return target.ID, nil
		}
	}
	return "", errors.New("browser has no open page")
}

func openPage(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("URL must use http or https")
	}
	if parsed.User != nil {
		return "", errors.New("URL credentials are not allowed")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return "", errors.New("URL host cannot be resolved")
	}
	for _, address := range addresses {
		if blockedIP(address.IP) {
			return "", errors.New("local and private network URLs are not allowed")
		}
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(parsed.String()), chromedp.WaitReady("body")); err != nil {
		return "", fmt.Errorf("open page: %w", err)
	}
	return "Opened " + parsed.String(), nil
}

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func readPage(ctx context.Context) (string, error) {
	var title string
	var location string
	var text string
	if err := chromedp.Run(ctx, chromedp.Title(&title), chromedp.Location(&location), chromedp.Evaluate(`document.body.innerText`, &text)); err != nil {
		return "", fmt.Errorf("read page: %w", err)
	}
	text = strings.TrimSpace(text)
	if len([]rune(text)) > maxOutput {
		text = string([]rune(text)[:maxOutput])
	}
	return fmt.Sprintf("Title: %s\nURL: %s\n\n%s", title, location, text), nil
}

func readLinks(ctx context.Context) (string, error) {
	var links []link
	expression := `Array.from(document.querySelectorAll('a[href]')).filter(a => a.offsetParent !== null).slice(0, 100).map(a => ({text: a.innerText.trim(), url: a.href}))`
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &links)); err != nil {
		return "", fmt.Errorf("read links: %w", err)
	}
	data, err := json.Marshal(links)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func screenshot(ctx context.Context, name string) (string, error) {
	if !validName.MatchString(name) {
		return "", errors.New("name must use letters, numbers, underscores, or hyphens")
	}
	directory := os.Getenv("BROWSERX_ARTIFACT_DIR")
	if directory == "" {
		return "", errors.New("BROWSERX_ARTIFACT_DIR is required")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", err
	}
	var image []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&image)); err != nil {
		return "", fmt.Errorf("take screenshot: %w", err)
	}
	path := filepath.Join(directory, name+".png")
	if err := os.WriteFile(path, image, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func fail(code int, message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(code)
}
