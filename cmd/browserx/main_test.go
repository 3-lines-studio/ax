package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	fn()
	os.Stdout = old
	_ = writer.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, reader)
	_ = reader.Close()
	return buf.String()
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestMainDescribe(t *testing.T) {
	for _, args := range [][]string{
		{"browserx", "describe"},
		{"browserx", "--", "describe"},
	} {
		oldArgs := os.Args
		os.Args = args
		out := captureStdout(t, main)
		os.Args = oldArgs
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 4 {
			t.Fatalf("args %v: expected 4 tool specs, got %d", args, len(lines))
		}
	}
}

func TestPrintTools(t *testing.T) {
	out := captureStdout(t, printTools)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 tool specs, got %d", len(lines))
	}
	type toolSpec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  struct {
			Properties map[string]struct{} `json:"properties"`
			Required   []string            `json:"required"`
		} `json:"parameters"`
	}
	got := make(map[string]toolSpec)
	for _, line := range lines {
		var spec toolSpec
		if err := json.Unmarshal([]byte(line), &spec); err != nil {
			t.Fatalf("invalid tool spec %q: %v", line, err)
		}
		if spec.Name == "" || spec.Description == "" {
			t.Fatalf("missing name or description in %q", line)
		}
		if spec.Parameters.Properties == nil {
			t.Fatalf("missing parameters in %q", line)
		}
		got[spec.Name] = spec
	}
	for _, name := range []string{"browser_open", "browser_read", "browser_links", "browser_screenshot"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing tool %q", name)
		}
	}
	if !contains(got["browser_open"].Parameters.Required, "url") {
		t.Fatal("browser_open should require url")
	}
	if !contains(got["browser_screenshot"].Parameters.Required, "name") {
		t.Fatal("browser_screenshot should require name")
	}
	if len(got["browser_read"].Parameters.Required) != 0 {
		t.Fatal("browser_read should require no parameters")
	}
	if len(got["browser_links"].Parameters.Required) != 0 {
		t.Fatal("browser_links should require no parameters")
	}
}

func TestMainArgParsingRun(t *testing.T) {
	cases := []struct {
		args     []string
		stdin    string
		code     int
		contains string
	}{
		{[]string{"run"}, "", 2, "usage: browserx"},
		{[]string{"run", "bogus"}, "{}", 2, "unknown tool: bogus"},
		{[]string{"run", "browser_open"}, "not-json", 2, "invalid arguments"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestMainHelper")
			cmd.Env = append(os.Environ(),
				"TEST_MAIN_HELPER=1",
				"TEST_MAIN_ARGS="+strings.Join(tc.args, "|"),
				"TEST_MAIN_STDIN="+tc.stdin,
			)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("expected exit error, got %v (out=%s)", err, out.String())
			}
			if exitErr.ExitCode() != tc.code {
				t.Fatalf("exit code %d, want %d (out=%s)", exitErr.ExitCode(), tc.code, out.String())
			}
			if !strings.Contains(out.String(), tc.contains) {
				t.Fatalf("output %q missing %q", out.String(), tc.contains)
			}
		})
	}
}

func TestMainHelper(t *testing.T) {
	if os.Getenv("TEST_MAIN_HELPER") != "1" {
		t.Skip("helper")
	}
	os.Args = append([]string{"browserx"}, strings.Split(os.Getenv("TEST_MAIN_ARGS"), "|")...)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(writer, os.Getenv("TEST_MAIN_STDIN"))
	_ = writer.Close()
	os.Stdin = reader
	main()
}

func TestOpenPageRejectsUnsafeURLs(t *testing.T) {
	for _, value := range []string{
		"file:///etc/passwd",
		"https://user:pass@example.com",
		"not-a-url",
		"",
		"http://",
		"https://",
		"mailto:user@example.com",
		"data:text/plain,hi",
		"javascript:alert(1)",
		"http://user@example.com",
	} {
		if _, err := openPage(t.Context(), value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestOpenPageURLAllowlist(t *testing.T) {
	for _, value := range []string{
		"ftp://example.com",
		"ws://example.com",
		"ssh://host",
		"gopher://example.com",
		"git://host/repo",
	} {
		if _, err := openPage(t.Context(), value); err == nil {
			t.Fatalf("accepted non-http scheme %q", value)
		}
	}
}

func TestBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"10.0.0.1",
		"192.168.1.1",
		"169.254.1.1",
		"::1",
		"0.0.0.0",
		"::",
		"224.0.0.1",
		"ff02::1",
		"fe80::1",
		"172.16.5.5",
		"169.254.169.254",
	}
	for _, value := range blocked {
		if !blockedIP(net.ParseIP(value)) {
			t.Fatalf("did not block %s", value)
		}
	}
	public := []string{
		"1.1.1.1",
		"8.8.8.8",
		"2001:4860:4860::8888",
		"93.184.216.34",
	}
	for _, value := range public {
		if blockedIP(net.ParseIP(value)) {
			t.Fatalf("blocked public address %s", value)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, value := range []string{
		"a",
		"A",
		"0",
		"hello",
		"page-1",
		"page_2",
		"aB3-c_D",
		strings.Repeat("a", 64),
	} {
		if !validName.MatchString(value) {
			t.Errorf("rejected valid name %q", value)
		}
	}
	for _, value := range []string{
		"",
		"_",
		"-",
		"a b",
		"a/b",
		"a.b",
		"a@b",
		"é",
		strings.Repeat("a", 65),
	} {
		if validName.MatchString(value) {
			t.Errorf("accepted invalid name %q", value)
		}
	}
}

func TestScreenshotRejectsInvalidName(t *testing.T) {
	for _, value := range []string{"../file", "a b", "a/b", ""} {
		if _, err := screenshot(t.Context(), value); err == nil {
			t.Fatalf("accepted invalid screenshot name %q", value)
		}
	}
}

func TestScreenshotRequiresArtifactDir(t *testing.T) {
	t.Setenv("BROWSERX_ARTIFACT_DIR", "")
	if _, err := screenshot(t.Context(), "page"); err == nil || !strings.Contains(err.Error(), "BROWSERX_ARTIFACT_DIR is required") {
		t.Fatalf("expected required artifact dir error, got %v", err)
	}
}

func TestCurrentTargetSelectsFirstPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"abc","type":"page"},{"id":"def","type":"background"}]`)
	}))
	defer server.Close()
	id, err := currentTarget(t.Context(), server.URL+"//")
	if err != nil {
		t.Fatalf("currentTarget: %v", err)
	}
	if id != "abc" {
		t.Fatalf("got id %q, want abc", id)
	}
}

func TestCurrentTargetNoPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"x","type":"background"}]`)
	}))
	defer server.Close()
	if _, err := currentTarget(t.Context(), server.URL); err == nil || !strings.Contains(err.Error(), "browser has no open page") {
		t.Fatalf("expected no-page error, got %v", err)
	}
}

func TestCurrentTargetEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	if _, err := currentTarget(t.Context(), server.URL); err == nil || !strings.Contains(err.Error(), "browser has no open page") {
		t.Fatalf("expected no-page error, got %v", err)
	}
}

func TestCurrentTargetNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := currentTarget(t.Context(), server.URL); err == nil || !strings.Contains(err.Error(), "list browser pages") {
		t.Fatalf("expected listing error, got %v", err)
	}
}

func TestCurrentTargetInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `not-json`)
	}))
	defer server.Close()
	if _, err := currentTarget(t.Context(), server.URL); err == nil || !strings.Contains(err.Error(), "list browser pages") {
		t.Fatalf("expected decode error, got %v", err)
	}
}
