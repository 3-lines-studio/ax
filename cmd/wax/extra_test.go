package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestMain(m *testing.M) {
	if os.Getenv("WAX_CLI_HELPER") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String()
}

func cliEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "WAX_CLI_HELPER=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "WAX_CLI_HELPER=1")
}

func runCLI(t *testing.T, args []string, stdin string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = cliEnv()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running CLI: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

func htmlServer(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestConvertHeadings(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := "<h1>One</h1><h2>Two</h2><h3>Three</h3><h4>Four</h4><h5>Five</h5><h6>Six</h6>"
	output, _, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# One", "## Two", "### Three", "#### Four", "##### Five", "###### Six"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
}

func TestConvertLists(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := "<ul><li>alpha</li><li>beta<ul><li>gamma</li></ul></li></ul><ol><li>one</li><li>two</li></ol>"
	output, _, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- alpha", "- beta", "- gamma", "1. one", "2. two"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "## Links") {
		t.Errorf("list-only page must not produce a Links section:\n%s", output)
	}
}

func TestConvertCodeBlocks(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := "<pre><code>package main\n\nfunc main() {}\n</code></pre><p>call <code>go test</code> now</p>"
	output, _, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"```", "package main", "func main() {}", "`go test`"} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
}

func TestConvertStripsElements(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := `<script>SECRET_SCRIPT</script><style>SECRET_STYLE</style><noscript>SECRET_NOSCRIPT</noscript><svg>SECRET_SVG</svg><canvas>SECRET_CANVAS</canvas><nav>SECRET_NAV</nav><footer>SECRET_FOOTER</footer><form>SECRET_FORM</form><dialog>SECRET_DIALOG</dialog><template>SECRET_TEMPLATE</template><div hidden>SECRET_HIDDEN</div><span aria-hidden="true">SECRET_ARIA</span><main><p>VisibleMainContent</p></main>`
	output, _, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "VisibleMainContent") {
		t.Errorf("visible content was dropped:\n%s", output)
	}
	for _, secret := range []string{"SECRET_SCRIPT", "SECRET_STYLE", "SECRET_NOSCRIPT", "SECRET_SVG", "SECRET_CANVAS", "SECRET_NAV", "SECRET_FOOTER", "SECRET_FORM", "SECRET_DIALOG", "SECRET_TEMPLATE", "SECRET_HIDDEN", "SECRET_ARIA"} {
		if strings.Contains(output, secret) {
			t.Errorf("output leaked stripped element %q:\n%s", secret, output)
		}
	}
}

func TestConvertStripsToEmpty(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := "<script>a</script><nav>b</nav><style>c</style>"
	output, textLength, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != "" {
		t.Errorf("expected empty output, got:\n%q", output)
	}
	if textLength != 0 {
		t.Errorf("expected zero text length, got %d", textLength)
	}
}

func TestConvertAnchorWithoutHref(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := "<a>bare anchor</a><p>keep</p>"
	output, _, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "bare anchor") {
		t.Errorf("anchor text missing:\n%s", output)
	}
	if strings.Contains(output, "[bare") {
		t.Errorf("anchor without href became a link:\n%s", output)
	}
	if strings.Contains(output, "## Links") {
		t.Errorf("anchor without href must not be collected as a link:\n%s", output)
	}
}

func TestConvertImageRelativeSrc(t *testing.T) {
	pageURL, err := url.Parse("https://example.com/docs/page")
	if err != nil {
		t.Fatal(err)
	}
	input := "<p>x</p><img src=\"img/pic.png\" alt=\"Pic\">"
	output, _, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "![Pic](https://example.com/docs/img/pic.png)") {
		t.Errorf("image src was not resolved against pageURL:\n%s", output)
	}
}

func TestConvertVisibleTextLength(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, textLength, err := convert("<p>Hello   world</p>", pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if textLength != len("Hello world") {
		t.Errorf("visible text length = %d, want %d", textLength, len("Hello world"))
	}
}

func TestConvertBlockquoteAndRule(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	input := "<blockquote><p>quoted</p></blockquote><hr>"
	output, _, err := convert(input, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "> quoted") {
		t.Errorf("blockquote not rendered:\n%s", output)
	}
	if !strings.Contains(output, "* * *") {
		t.Errorf("thematic break not rendered:\n%s", output)
	}
}

func TestRemoveNode(t *testing.T) {
	for _, tag := range []string{"script", "style", "noscript", "svg", "canvas", "nav", "footer", "form", "dialog", "template"} {
		node := &html.Node{Type: html.ElementNode, Data: tag}
		if !removeNode(node) {
			t.Errorf("removeNode(%q) = false, want true", tag)
		}
	}
	for _, tag := range []string{"div", "p", "span", "main", "ul", "li", "a", "img", "h1", "pre", "code"} {
		node := &html.Node{Type: html.ElementNode, Data: tag}
		if removeNode(node) {
			t.Errorf("removeNode(%q) = true, want false", tag)
		}
	}
	if !removeNode(&html.Node{Type: html.ElementNode, Data: "div", Attr: []html.Attribute{{Key: "hidden"}}}) {
		t.Error("hidden element not removed")
	}
	if !removeNode(&html.Node{Type: html.ElementNode, Data: "div", Attr: []html.Attribute{{Key: "aria-hidden", Val: "true"}}}) {
		t.Error("aria-hidden=true element not removed")
	}
	if removeNode(&html.Node{Type: html.ElementNode, Data: "div", Attr: []html.Attribute{{Key: "aria-hidden", Val: "false"}}}) {
		t.Error("aria-hidden=false element wrongly removed")
	}
}

func TestVisibleText(t *testing.T) {
	doc, err := html.Parse(strings.NewReader("<div>a  \n\t b <span>c</span></div>"))
	if err != nil {
		t.Fatal(err)
	}
	body := findContent(doc)
	text := visibleText(body)
	if text != "a b c" {
		t.Errorf("visibleText = %q, want %q", text, "a b c")
	}
}

func TestFindContent(t *testing.T) {
	doc, err := html.Parse(strings.NewReader("<html><body><p>x</p></body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	body := doc
	for child := doc.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "html" {
			for gc := child.FirstChild; gc != nil; gc = gc.NextSibling {
				if gc.Type == html.ElementNode && gc.Data == "body" {
					body = gc
				}
			}
		}
	}
	if found := findContent(doc); found != body {
		t.Error("findContent did not select the body element")
	}

	root := &html.Node{Type: html.ElementNode, Data: "div"}
	if found := findContent(root); found != root {
		t.Error("findContent did not fall back to the root when no body exists")
	}
}

func TestCollectLinks(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<a href="https://ex.com/a">A</a><a href="https://ex.com/a">A2</a><a href="/rel" title="T">R</a><a href="mailto:a@b.c">M</a><a href="javascript:void(0)">J</a><a href="#frag">F</a><a href="ftp://x">FTP</a><a href="http://[::1">Bad</a>`))
	if err != nil {
		t.Fatal(err)
	}
	links := collectLinks(doc)
	want := []string{"https://ex.com/a"}
	if !reflect.DeepEqual(links, want) {
		t.Errorf("collectLinks = %v, want %v", links, want)
	}
}

func TestChromiumPathMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := chromiumPath(); err == nil {
		t.Error("expected error when no chromium binary is on PATH")
	}
}

func TestChromiumPathFound(t *testing.T) {
	dir := t.TempDir()
	name := dir + "/chromium"
	if err := os.WriteFile(name, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	path, err := chromiumPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != name {
		t.Errorf("chromiumPath = %q, want %q", path, name)
	}
}

func TestFetchStaticHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fetchStatic(context.Background(), pageURL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchStaticUnsupportedContentType(t *testing.T) {
	server := htmlServer(t, "application/json", `{"a":1}`)
	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fetchStatic(context.Background(), pageURL)
	if err == nil || err.Error() != "unsupported content type application/json" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchStaticPageTooLarge(t *testing.T) {
	server := htmlServer(t, "text/html", strings.Repeat("a", maxHTML+1))
	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fetchStatic(context.Background(), pageURL)
	if err == nil || err.Error() != "page exceeds 10 MiB" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchStaticRequestError(t *testing.T) {
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = fetchStatic(ctx, pageURL)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestFetchSparseStaticFallsBackToRender(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := htmlServer(t, "text/html", "<main><p>too short</p></main>")
	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = fetch(ctx, pageURL)
	if err == nil {
		t.Fatal("expected fetch to fall back to unavailable render")
	}
	if !strings.Contains(err.Error(), "rendered fetch") {
		t.Errorf("expected rendered-fetch error, got %v", err)
	}
}

func TestFetchStaticErrorFallsBackToRender(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = fetch(ctx, pageURL)
	if err == nil {
		t.Fatal("expected fetch to fall back to unavailable render")
	}
	msg := err.Error()
	if !strings.Contains(msg, "static fetch: HTTP 404") {
		t.Errorf("expected combined static error, got %v", err)
	}
	if !strings.Contains(msg, "rendered fetch") {
		t.Errorf("expected rendered-fetch error, got %v", err)
	}
}

func TestFetchUnsupportedContentTypeFallsBackToRender(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := htmlServer(t, "application/octet-stream", "binary")
	pageURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = fetch(ctx, pageURL)
	if err == nil {
		t.Fatal("expected fetch to fall back to unavailable render")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unsupported content type") {
		t.Errorf("expected content-type error, got %v", err)
	}
	if !strings.Contains(msg, "rendered fetch") {
		t.Errorf("expected rendered-fetch error, got %v", err)
	}
}

func TestRunSuccess(t *testing.T) {
	text := strings.Repeat("padding text ", 100)
	server := htmlServer(t, "text/html", "<main><h1>Via Run</h1><p>"+text+"</p></main>")
	out := captureStdout(t, func() { run(server.URL) })
	if !strings.Contains(out, "# Via Run") {
		t.Errorf("run output missing heading:\n%s", out)
	}
}

func TestMainDescribeInProcess(t *testing.T) {
	oldArgs := os.Args
	os.Args = []string{"wax", "describe"}
	defer func() { os.Args = oldArgs }()
	out := captureStdout(t, func() { main() })
	var spec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(out), &spec); err != nil {
		t.Fatalf("describe output is not valid JSON: %v\n%s", err, out)
	}
	if spec.Name != "web_fetch" {
		t.Errorf("name = %q, want web_fetch", spec.Name)
	}
	if spec.Description == "" {
		t.Error("description is empty")
	}
	if _, ok := spec.Parameters.Properties["url"]; !ok {
		t.Error("parameters lack a url property")
	}
	if !reflect.DeepEqual(spec.Parameters.Required, []string{"url"}) {
		t.Errorf("required = %v, want [url]", spec.Parameters.Required)
	}
}

func TestCliDescribe(t *testing.T) {
	out, errOut, code := runCLI(t, []string{"describe"}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut)
	}
	if errOut != "" {
		t.Errorf("unexpected stderr: %s", errOut)
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(out), &spec); err != nil {
		t.Fatalf("describe output is not JSON: %v\n%s", err, out)
	}
	if spec["name"] != "web_fetch" {
		t.Errorf("name = %v, want web_fetch", spec["name"])
	}
	required, ok := spec["parameters"].(map[string]any)["required"]
	if !ok {
		t.Fatal("describe parameters lack required")
	}
	req, _ := required.([]any)
	if len(req) != 1 || req[0] != "url" {
		t.Errorf("required = %v, want [url]", required)
	}
}

func TestCliRunWebFetchMissingURL(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"run", "web_fetch"}, `{}`)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "URL must use http or https") {
		t.Errorf("stderr = %q, want URL validation message", errOut)
	}
}

func TestCliRunWebFetchInvalidJSON(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"run", "web_fetch"}, "not json")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "invalid arguments") {
		t.Errorf("stderr = %q, want invalid-arguments message", errOut)
	}
}

func TestCliRunWebFetchBadScheme(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"run", "web_fetch"}, `{"url":"ftp://host/x"}`)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "URL must use http or https") {
		t.Errorf("stderr = %q, want URL validation message", errOut)
	}
}

func TestCliRunBadValueArg(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"notaurl"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "URL must use http or https") {
		t.Errorf("stderr = %q, want URL validation message", errOut)
	}
}

func TestCliRunTooManyArgs(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"a", "b"}, "")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "usage") {
		t.Errorf("stderr = %q, want usage message", errOut)
	}
}

func TestFetchRenderedWAXNoSandbox(t *testing.T) {
	if _, err := chromiumPath(); err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Setenv("WAX_NO_SANDBOX", "1")
	pageURL, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fetchRendered(ctx, pageURL); err == nil {
		t.Fatal("expected render to fail with cancelled context")
	}
}

func TestCliRunWebFetchSuccess(t *testing.T) {
	text := strings.Repeat("padding text ", 100)
	server := htmlServer(t, "text/html", "<main><h1>Via CLI</h1><p>"+text+"</p></main>")
	payload := `{"url":"` + server.URL + `"}`
	out, errOut, code := runCLI(t, []string{"run", "web_fetch"}, payload)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "# Via CLI") {
		t.Errorf("CLI output missing heading:\n%s", out)
	}
}

func TestCliRunWebFetchFetchError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	payload := `{"url":"` + server.URL + `"}`
	_, errOut, code := runCLI(t, []string{"run", "web_fetch"}, payload)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "error:") {
		t.Errorf("stderr = %q, want error prefix", errOut)
	}
}

func TestCliRunDoubleDash(t *testing.T) {
	text := strings.Repeat("padding text ", 100)
	server := htmlServer(t, "text/html", "<main><h1>Via Dash</h1><p>"+text+"</p></main>")
	out, errOut, code := runCLI(t, []string{"--", server.URL}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "# Via Dash") {
		t.Errorf("CLI output missing heading:\n%s", out)
	}
}
