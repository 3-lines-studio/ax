package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	defaultSearchLimit = 8
	maxSearchLimit     = 20
	maxMenuSamples     = 12
)

// Menu renders the whole index: every server, its tools and how to use the
// two catalog tools. It is the fallback for an empty or unsuccessful search.
func (c *Catalog) Menu() string {
	var out strings.Builder
	out.WriteString("# Catalog\n\n")
	servers := c.Servers()
	if len(servers) == 0 {
		out.WriteString("No tool sources configured. Add `[[mcp]]` servers to bot.toml or providers to AX_TOOLS.\n")
		return out.String()
	}
	out.WriteString("## Servers\n\n")
	for _, server := range servers {
		if server.AuthRequired {
			fmt.Fprintf(&out, "- **%s** - login required: run `ax mcp login %s`\n", server.Name, server.Name)
			continue
		}
		if server.Error != "" {
			fmt.Fprintf(&out, "- **%s** - unavailable: %s\n", server.Name, oneLine(server.Error, 160))
			continue
		}
		description := oneLine(server.Description, 120)
		label := fmt.Sprintf("- **%s** (%d %s)", server.Name, server.Tools, plural(server.Tools, "tool"))
		if description != "" {
			label += ": " + description
		}
		out.WriteString(label + "\n")
		names := make([]string, 0, server.Tools)
		for _, tool := range c.tools {
			if tool.Server == server.Name {
				names = append(names, "`"+tool.ID+"`")
			}
		}
		if len(names) > maxMenuSamples {
			names = append(names[:maxMenuSamples], fmt.Sprintf("+%d more", len(names)-maxMenuSamples))
		}
		if len(names) > 0 {
			fmt.Fprintf(&out, "  %s\n", strings.Join(names, ", "))
		}
	}
	out.WriteString("\n## Use\n\n")
	out.WriteString("- `search({\"query\":\"what you want\"})` ranks matching tools.\n")
	out.WriteString("- `search({\"server\":\"name\"})` lists that server's tools.\n")
	out.WriteString("- `search({\"tool\":\"server.tool\"})` returns the full schema and an example call.\n")
	out.WriteString("- `execute({\"tool\":\"server.tool\",\"arguments\":{...}})` runs one tool.\n")
	return out.String()
}

// Brief renders the small catalog summary that goes into the system prompt.
func (c *Catalog) Brief(core []string) string {
	var out strings.Builder
	servers := c.Servers()
	var parts []string
	for _, server := range servers {
		if server.AuthRequired {
			parts = append(parts, fmt.Sprintf("%s (login required)", server.Name))
			continue
		}
		if server.Error != "" {
			parts = append(parts, fmt.Sprintf("%s (unavailable)", server.Name))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", server.Name, server.Tools))
	}
	if len(parts) == 0 {
		out.WriteString("Catalog: no tools configured.\n")
	} else {
		fmt.Fprintf(&out, "Catalog servers: %s.\n", strings.Join(parts, ", "))
	}
	out.WriteString("Use search to inspect a tool and execute to run one. Search with no arguments returns the whole catalog.\n")
	if len(core) > 0 {
		var lines []string
		for _, id := range core {
			tool, ok := c.Lookup(id)
			if !ok {
				continue
			}
			lines = append(lines, "- `"+tool.ID+"`: "+oneLine(tool.Description, 120)+" Args: "+schemaArgs(tool, 4))
		}
		if len(lines) > 0 {
			out.WriteString("\nCore tools you may call directly with execute:\n")
			out.WriteString(strings.Join(lines, "\n"))
			out.WriteString("\n")
		}
	}
	return out.String()
}

// Search handles the search tool: menu, ranking, one server, or one tool.
func (c *Catalog) Search(request SearchRequest) string {
	limit := request.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	if strings.TrimSpace(request.Tool) != "" {
		if tool, ok := c.Lookup(request.Tool); ok {
			return c.detail(tool)
		}
		suggestions := c.Suggest(request.Tool, 5)
		var out strings.Builder
		fmt.Fprintf(&out, "Unknown tool %q.\n\n", request.Tool)
		if len(suggestions) > 0 {
			fmt.Fprintf(&out, "Closest matches: %s\n\n", quoteList(suggestions))
		}
		out.WriteString(c.Menu())
		return out.String()
	}
	if strings.TrimSpace(request.Server) != "" {
		if strings.TrimSpace(request.Query) == "" {
			return c.serverMenu(request.Server, limit)
		}
		return c.search(request.Query, request.Server, limit)
	}
	if strings.TrimSpace(request.Query) == "" {
		return c.Menu()
	}
	return c.search(request.Query, "", limit)
}

func (c *Catalog) search(query, server string, limit int) string {
	matches := c.rank(query, server)
	var out strings.Builder
	if len(matches) == 0 {
		fmt.Fprintf(&out, "No strong match for %q.\n\n", query)
		if suggestions := c.Suggest(query, 5); len(suggestions) > 0 {
			fmt.Fprintf(&out, "Closest names: %s\n\n", quoteList(suggestions))
		}
		// A query that names a server should still open that server.
		for _, server := range c.Servers() {
			if strings.EqualFold(server.Name, query) {
				out.WriteString(c.serverMenu(server.Name, limit))
				return out.String()
			}
		}
		out.WriteString(c.Menu())
		return out.String()
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	fmt.Fprintf(&out, "## Matches for %q\n\n", query)
	for _, tool := range matches {
		out.WriteString(c.section(tool))
		out.WriteString("\n")
	}
	out.WriteString("## More\n\n")
	out.WriteString("- `search({\"server\":\"name\"})` lists that server's tools.\n")
	out.WriteString("- `search({\"tool\":\"server.tool\"})` returns the full schema.\n")
	out.WriteString("- `search({})` returns the whole catalog.\n")
	return out.String()
}

func (c *Catalog) serverMenu(name string, limit int) string {
	for _, server := range c.Servers() {
		if !strings.EqualFold(server.Name, name) {
			continue
		}
		var out strings.Builder
		fmt.Fprintf(&out, "## Server %s\n\n", server.Name)
		if server.Description != "" {
			out.WriteString(oneLine(server.Description, 300) + "\n\n")
		}
		if server.AuthRequired {
			fmt.Fprintf(&out, "Login required: run `ax mcp login %s`.\n", server.Name)
		} else if server.Error != "" {
			fmt.Fprintf(&out, "Unavailable: %s\n", oneLine(server.Error, 300))
			return out.String()
		}
		indexes := c.byServer[server.Name]
		if len(indexes) == 0 {
			out.WriteString("This server exposes no tools.\n")
			return out.String()
		}
		shown := 0
		for _, index := range indexes {
			if limit > 0 && shown == limit*5 {
				fmt.Fprintf(&out, "_...and %d more tools; refine with search({\"query\":\"...\",\"server\":%q})._\n", len(indexes)-shown, server.Name)
				break
			}
			out.WriteString(c.section(c.tools[index]))
			out.WriteString("\n")
			shown++
		}
		return out.String()
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Unknown server %q.\n\n", name)
	out.WriteString(c.Menu())
	return out.String()
}

func (c *Catalog) detail(tool Tool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "### `%s` - %s\n\n", tool.ID, tool.Title)
	if tool.Description != "" {
		out.WriteString(oneLine(tool.Description, 600) + "\n\n")
	}
	fmt.Fprintf(&out, "Server: %s", tool.Server)
	if tags := toolTags(tool); tags != "" {
		out.WriteString(" - " + tags)
	}
	out.WriteString("\n\nInput schema:\n\n```json\n")
	out.WriteString(prettyJSON(tool.InputSchema))
	out.WriteString("\n```\n\n")
	out.WriteString("Args: " + schemaArgs(tool, 40) + "\n\n")
	fmt.Fprintf(&out, "Run: `execute(%s)`\n", executeExample(tool))
	return out.String()
}

func (c *Catalog) section(tool Tool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "### `%s` - %s\n", tool.ID, tool.Title)
	if tool.Description != "" {
		out.WriteString(oneLine(tool.Description, 240) + "\n")
	}
	meta := "Server: " + tool.Server
	if tags := toolTags(tool); tags != "" {
		meta += " - " + tags
	}
	out.WriteString(meta + "\n")
	out.WriteString("Args: " + schemaArgs(tool, 12) + "\n")
	fmt.Fprintf(&out, "Run: `execute(%s)`\n", executeExample(tool))
	return out.String()
}

func toolTags(tool Tool) string {
	var tags []string
	if tool.Annotations.ReadOnly {
		tags = append(tags, "read-only")
	}
	if tool.Annotations.Destructive {
		tags = append(tags, "destructive")
	}
	if tool.Annotations.Idempotent {
		tags = append(tags, "idempotent")
	}
	if tool.Annotations.OpenWorld {
		tags = append(tags, "open-world")
	}
	if tool.Annotations.Sequential && !tool.Annotations.ReadOnly {
		tags = append(tags, "sequential")
	}
	return strings.Join(tags, ", ")
}

func (c *Catalog) rank(query, server string) []Tool {
	tokens := queryTokens(query)
	if len(tokens) == 0 {
		return nil
	}
	type scored struct {
		tool  Tool
		score int
	}
	var matches []scored
	for _, tool := range c.tools {
		if server != "" && !strings.EqualFold(tool.Server, server) {
			continue
		}
		if score := searchScore(tool, tokens, query); score > 0 {
			matches = append(matches, scored{tool: tool, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].tool.ID < matches[j].tool.ID
	})
	out := make([]Tool, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.tool)
	}
	return out
}

func searchScore(tool Tool, tokens []string, query string) int {
	name := strings.ToLower(tool.Name)
	id := strings.ToLower(tool.ID)
	title := strings.ToLower(tool.Title)
	description := strings.ToLower(tool.Description)
	server := strings.ToLower(tool.Server)
	query = strings.ToLower(strings.TrimSpace(query))
	spacedID := strings.ReplaceAll(id, ".", " ")

	score := 0
	switch {
	case id == query || name == query:
		score += 1000
	case strings.HasPrefix(id, query) || strings.HasPrefix(name, query):
		score += 800
	case strings.Contains(id, query) || strings.Contains(spacedID, query):
		score += 600
	case strings.Contains(description, query):
		score += 200
	}
	allInName := true
	for _, token := range tokens {
		hit := false
		if strings.Contains(name, token) {
			score += 120
			hit = true
		}
		if strings.Contains(title, token) {
			score += 50
			hit = true
		}
		if strings.Contains(description, token) {
			score += 20
			hit = true
		}
		if strings.Contains(server, token) {
			score += 30
			hit = true
		}
		if !hit {
			allInName = false
		}
	}
	if allInName && len(tokens) > 1 {
		score += 200
	}
	if score == 0 {
		score = fuzzyNameScore(query, name)
	}
	return score
}

func fuzzyNameScore(query, name string) int {
	best := levenshtein(query, name)
	for _, word := range strings.Fields(strings.ReplaceAll(strings.ReplaceAll(name, "_", " "), "-", " ")) {
		if distance := levenshtein(query, word); distance < best {
			best = distance
		}
	}
	if best <= 2 || best <= len(name)/3 {
		return 300 - 40*best
	}
	return 0
}

func queryTokens(query string) []string {
	query = strings.ToLower(query)
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		token := current.String()
		current.Reset()
		if stopwords[token] {
			return
		}
		tokens = append(tokens, token)
		if strings.HasSuffix(token, "s") && len(token) > 3 {
			singular := strings.TrimSuffix(token, "s")
			if singular != token {
				tokens = append(tokens, singular)
			}
		}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "can": true, "do": true, "for": true, "from": true,
	"i": true, "in": true, "is": true, "it": true, "me": true, "my": true,
	"of": true, "on": true, "or": true, "the": true, "to": true, "use": true,
	"what": true, "with": true, "you": true,
}

func schemaArgs(tool Tool, max int) string {
	var schema struct {
		Properties map[string]struct {
			Type        any    `json:"type"`
			Description string `json:"description"`
			Enum        []any  `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || len(schema.Properties) == 0 {
		return "none"
	}
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		if max > 0 && len(parts) == max {
			parts = append(parts, "...")
			break
		}
		property := schema.Properties[name]
		kind := schemaType(property.Type, property.Enum)
		part := fmt.Sprintf("`%s` (%s", name, kind)
		if required[name] {
			part += ", required"
		}
		part += ")"
		if property.Description != "" {
			part += " - " + oneLine(property.Description, 70)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func schemaType(value any, enum []any) string {
	switch typed := value.(type) {
	case string:
		if len(enum) > 0 {
			return typed + " enum"
		}
		return typed
	case []any:
		var kinds []string
		for _, item := range typed {
			if text, ok := item.(string); ok {
				kinds = append(kinds, text)
			}
		}
		if len(kinds) > 0 {
			return strings.Join(kinds, "|")
		}
	}
	return "any"
}

func executeExample(tool Tool) string {
	return fmt.Sprintf(`{"tool":%q,"arguments":%s}`, tool.ID, exampleArgs(tool))
}

func exampleArgs(tool Tool) string {
	var schema struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		return "{}"
	}
	args := map[string]any{}
	for _, name := range schema.Required {
		property, ok := schema.Properties[name]
		if !ok {
			args[name] = "..."
			continue
		}
		kind, _ := property.Type.(string)
		switch kind {
		case "integer", "number":
			args[name] = 0
		case "boolean":
			args[name] = false
		case "array":
			args[name] = []any{}
		case "object":
			args[name] = map[string]any{}
		default:
			args[name] = "..."
		}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func prettyJSON(raw []byte) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(data)
}

func titleFromName(name string) string {
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	if name == "" {
		return name
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func oneLine(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max > 0 && len([]rune(value)) > max {
		runes := []rune(value)
		value = string(runes[:max]) + "..."
	}
	return value
}

func plural(count int, word string) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

func quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "`" + value + "`"
	}
	return strings.Join(quoted, ", ")
}

func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	previous := make([]int, len(br)+1)
	current := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		previous[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		current[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			current[j] = min3(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(br)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
