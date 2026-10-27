package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeSource struct {
	name   string
	desc   string
	tools  []Tool
	err    error
	called []string
}

func (s *fakeSource) Name() string        { return s.name }
func (s *fakeSource) Description() string { return s.desc }
func (s *fakeSource) Close() error        { return nil }
func (s *fakeSource) List(context.Context) ([]Tool, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tools, nil
}
func (s *fakeSource) Call(_ context.Context, name string, args json.RawMessage) (Result, error) {
	s.called = append(s.called, name+" "+string(args))
	return Result{Text: "ran " + name}, nil
}

func testCatalog(t *testing.T) (*Catalog, *fakeSource) {
	t.Helper()
	fsx := &fakeSource{name: "fsx", desc: "Local files", tools: []Tool{
		{Name: "read", Description: "Read the contents of a file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"offset":{"type":"integer"}},"required":["path"]}`), Annotations: Annotations{ReadOnly: true}},
		{Name: "write", Description: "Write a file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`), Annotations: Annotations{Destructive: true, Sequential: true}},
	}}
	github := &fakeSource{name: "github", desc: "Issues and pull requests", tools: []Tool{
		{Name: "create_issue", Description: "Create an issue in a repository", InputSchema: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"},"title":{"type":"string"}},"required":["repo","title"]}`)},
		{Name: "list_issues", Description: "List issues in a repository", Annotations: Annotations{ReadOnly: true}},
	}}
	broken := &fakeSource{name: "home", desc: "Home automation", err: errors.New("connection refused")}
	cat := New([]Source{fsx, github, broken})
	cat.Load(context.Background())
	return cat, fsx
}

func TestMenuListsServersAndUsage(t *testing.T) {
	cat, _ := testCatalog(t)
	menu := cat.Menu()
	for _, want := range []string{"fsx", "github", "home", "unavailable", "read", "create_issue", "search({", "execute({"} {
		if !strings.Contains(menu, want) {
			t.Errorf("menu missing %q:\n%s", want, menu)
		}
	}
}

func TestSearchRanksAndIncludesCallShape(t *testing.T) {
	cat, _ := testCatalog(t)
	out := cat.Search(SearchRequest{Query: "create an issue"})
	if !strings.Contains(out, "github.create_issue") {
		t.Fatalf("search missed create_issue:\n%s", out)
	}
	if !strings.Contains(out, `execute({"tool":"github.create_issue"`) {
		t.Errorf("search missed execute example:\n%s", out)
	}
	if !strings.Contains(out, "`repo` (string, required)") {
		t.Errorf("search missed args:\n%s", out)
	}
}

func TestSearchFuzzyAndNeverEmpty(t *testing.T) {
	cat, _ := testCatalog(t)
	out := cat.Search(SearchRequest{Query: "isues"})
	if !strings.Contains(out, "github.list_issues") && !strings.Contains(out, "github.create_issue") {
		t.Fatalf("fuzzy search found nothing:\n%s", out)
	}
	out = cat.Search(SearchRequest{Query: "zzzzzz"})
	if !strings.Contains(out, "No strong match") || !strings.Contains(out, "# Catalog") {
		t.Fatalf("bad search did not fall back to the menu:\n%s", out)
	}
}

func TestSearchServerAndTool(t *testing.T) {
	cat, _ := testCatalog(t)
	out := cat.Search(SearchRequest{Server: "github"})
	if !strings.Contains(out, "create_issue") || !strings.Contains(out, "list_issues") || strings.Contains(out, "fsx.read") {
		t.Fatalf("server menu wrong:\n%s", out)
	}
	out = cat.Search(SearchRequest{Tool: "fsx.read"})
	if !strings.Contains(out, "Input schema") || !strings.Contains(out, `"path"`) || !strings.Contains(out, "read-only") {
		t.Fatalf("tool detail wrong:\n%s", out)
	}
}

func TestBriefIncludesCoreTools(t *testing.T) {
	cat, _ := testCatalog(t)
	brief := cat.Brief([]string{"fsx.read", "missing.tool"})
	if !strings.Contains(brief, "fsx (2)") || !strings.Contains(brief, "github (2)") {
		t.Errorf("brief servers = %s", brief)
	}
	if !strings.Contains(brief, "`fsx.read`") || strings.Contains(brief, "missing.tool") {
		t.Errorf("brief core = %s", brief)
	}
}

func TestCallRoutesAndResolves(t *testing.T) {
	cat, fsx := testCatalog(t)
	result, err := cat.Call(context.Background(), "fsx.read", json.RawMessage(`{"path":"x"}`))
	if err != nil || result.Text != "ran read" {
		t.Fatalf("call = %+v err=%v", result, err)
	}
	if len(fsx.called) != 1 || !strings.Contains(fsx.called[0], `{"path":"x"}`) {
		t.Errorf("source calls = %v", fsx.called)
	}
	if _, err := cat.Call(context.Background(), "github.delete_everything", nil); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("unknown call err = %v", err)
	}
}

func TestSuggestReturnsNearest(t *testing.T) {
	cat, _ := testCatalog(t)
	suggestions := cat.Suggest("creat_issue", 3)
	if len(suggestions) == 0 || suggestions[0] != "github.create_issue" {
		t.Errorf("suggestions = %v", suggestions)
	}
}
