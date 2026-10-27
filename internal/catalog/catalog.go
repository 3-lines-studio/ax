package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Catalog is the loaded index of all configured sources.
type Catalog struct {
	sources  []Source
	tools    []Tool
	byID     map[string]int
	byName   map[string][]int
	byServer map[string][]int
	errs     map[string]string
	auth     map[string]bool
	loaded   bool
	mu       sync.Mutex
}

// New builds an empty catalog. Load must be called before Search or Call.
func New(sources []Source) *Catalog {
	return &Catalog{
		sources:  sources,
		byID:     map[string]int{},
		byName:   map[string][]int{},
		byServer: map[string][]int{},
		errs:     map[string]string{},
		auth:     map[string]bool{},
	}
}

// Load lists every source concurrently. A source that fails is recorded in
// the menu instead of failing the whole catalog: one broken MCP server must
// not take down every other integration.
func (c *Catalog) Load(ctx context.Context) {
	c.mu.Lock()
	if c.loaded {
		c.mu.Unlock()
		return
	}
	c.loaded = true
	c.mu.Unlock()

	type outcome struct {
		source Source
		tools  []Tool
		err    error
	}
	results := make(chan outcome, len(c.sources))
	var wg sync.WaitGroup
	for _, source := range c.sources {
		wg.Add(1)
		go func(source Source) {
			defer wg.Done()
			tools, err := source.List(ctx)
			results <- outcome{source: source, tools: tools, err: err}
		}(source)
	}
	wg.Wait()
	close(results)

	for result := range results {
		name := result.source.Name()
		if result.err != nil {
			message := result.err.Error()
			if ctx.Err() != nil {
				message = "catalog load cancelled: " + ctx.Err().Error()
			}
			c.errs[name] = oneLine(message, 160)
			if status, ok := result.source.(AuthStatus); ok && status.RequiresAuth() {
				c.auth[name] = true
			}
			continue
		}
		for _, tool := range result.tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}
			tool.Name = name
			tool.Server = result.source.Name()
			if tool.ID == "" {
				tool.ID = tool.Server + "." + name
			}
			if _, exists := c.byID[tool.ID]; exists {
				continue
			}
			if tool.Title == "" {
				tool.Title = titleFromName(name)
			}
			if len(tool.InputSchema) == 0 || string(tool.InputSchema) == "null" {
				tool.InputSchema = []byte(`{"type":"object"}`)
			}
			index := len(c.tools)
			c.tools = append(c.tools, tool)
			c.byID[tool.ID] = index
			c.byName[name] = append(c.byName[name], index)
			c.byServer[tool.Server] = append(c.byServer[tool.Server], index)
		}
	}
	sort.SliceStable(c.tools, func(i, j int) bool { return c.tools[i].ID < c.tools[j].ID })
	// Rebuild the lookups after sorting.
	c.byID = map[string]int{}
	c.byName = map[string][]int{}
	c.byServer = map[string][]int{}
	for index, tool := range c.tools {
		c.byID[tool.ID] = index
		c.byName[tool.Name] = append(c.byName[tool.Name], index)
		c.byServer[tool.Server] = append(c.byServer[tool.Server], index)
	}
}

// Close closes every source, returning the first error.
func (c *Catalog) Close() error {
	var first error
	for _, source := range c.sources {
		if err := source.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Tools returns the loaded tools in stable order.
func (c *Catalog) Tools() []Tool {
	out := make([]Tool, len(c.tools))
	copy(out, c.tools)
	return out
}

// Servers returns one summary per configured source, in configuration order.
func (c *Catalog) Servers() []ServerSummary {
	out := make([]ServerSummary, 0, len(c.sources))
	for _, source := range c.sources {
		out = append(out, ServerSummary{
			Name:         source.Name(),
			Description:  source.Description(),
			Tools:        len(c.byServer[source.Name()]),
			Error:        c.errs[source.Name()],
			AuthRequired: c.auth[source.Name()],
		})
	}
	return out
}

// ServerSummary is the menu view of one source.
type ServerSummary struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Tools        int    `json:"tools"`
	Error        string `json:"error,omitempty"`
	AuthRequired bool   `json:"auth_required,omitempty"`
}

// AuthStatus is an optional Source interface: a source whose last failure was
// a missing login reports it so the menu can name the exact command.
type AuthStatus interface {
	RequiresAuth() bool
}

// Lookup finds a tool by exact id, or by a unique bare tool name.
func (c *Catalog) Lookup(id string) (Tool, bool) {
	id = strings.TrimSpace(id)
	if index, ok := c.byID[id]; ok {
		return c.tools[index], true
	}
	if matches := c.byName[id]; len(matches) == 1 {
		return c.tools[matches[0]], true
	}
	return Tool{}, false
}

// Resolve is Lookup with suggestions when the id is unknown.
func (c *Catalog) Resolve(id string) (Tool, error) {
	if tool, ok := c.Lookup(id); ok {
		return tool, nil
	}
	suggestions := c.Suggest(id, 5)
	if len(suggestions) == 0 {
		return Tool{}, fmt.Errorf("unknown tool %q: call search() to see the catalog", id)
	}
	return Tool{}, fmt.Errorf("unknown tool %q; did you mean %s?", id, quoteList(suggestions))
}

// Call runs one tool by id.
func (c *Catalog) Call(ctx context.Context, id string, args []byte) (Result, error) {
	tool, err := c.Resolve(id)
	if err != nil {
		return Result{}, err
	}
	for _, source := range c.sources {
		if source.Name() == tool.Server {
			return source.Call(ctx, tool.Name, args)
		}
	}
	return Result{}, fmt.Errorf("tool %q belongs to unavailable server %q", id, tool.Server)
}

// Suggest returns the closest tool ids for a misspelled or partial name.
func (c *Catalog) Suggest(query string, limit int) []string {
	if limit <= 0 {
		limit = 5
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	type scored struct {
		id    string
		score int
	}
	var matches []scored
	for _, tool := range c.tools {
		score := 0
		name := strings.ToLower(tool.Name)
		id := strings.ToLower(tool.ID)
		switch {
		case id == query || name == query:
			score = 1000
		case strings.HasPrefix(id, query) || strings.HasPrefix(name, query):
			score = 800
		case strings.Contains(id, query):
			score = 600
		case strings.Contains(strings.ToLower(tool.Description), query):
			score = 200
		default:
			if fuzzy := fuzzyNameScore(query, name); fuzzy > 0 {
				score = fuzzy + 100
			}
		}
		if score > 0 {
			matches = append(matches, scored{id: tool.ID, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].id < matches[j].id
	})
	out := make([]string, 0, limit)
	for _, match := range matches {
		if len(out) == limit {
			break
		}
		out = append(out, match.id)
	}
	return out
}

// SourceDescription returns the configured description for a server.
func (c *Catalog) SourceDescription(name string) string {
	for _, source := range c.sources {
		if source.Name() == name {
			return source.Description()
		}
	}
	return ""
}
