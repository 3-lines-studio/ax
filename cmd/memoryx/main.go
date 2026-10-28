// memoryx exposes the bot's personal memory store through the Unix tool
// convention. It is a local provider indexed by ax's catalog next to fsx,
// bashx and skillx.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/3-lines-studio/ax/internal/botdir"
	"github.com/3-lines-studio/ax/internal/memory"
)

const maxInput = 64 << 10

var descriptors = []string{
	`{"name":"search","description":"Search saved personal memories. Use it for facts, preferences, people and project context the user may expect you to remember.","parameters":{"type":"object","properties":{"query":{"type":"string","description":"What to look for."},"limit":{"type":"integer","minimum":1,"maximum":20}},"additionalProperties":false},"read_only":true,"snippet":"Search personal memories"}`,
	`{"name":"get","description":"Read one saved memory by id, including its body and source links.","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false},"read_only":true,"snippet":"Read a memory"}`,
	`{"name":"verify","description":"Find memories already saved about a topic. Call this before upsert and report what you found.","parameters":{"type":"object","properties":{"topic":{"type":"string"}},"required":["topic"],"additionalProperties":false},"read_only":true,"snippet":"Check memories before saving"}`,
	`{"name":"upsert","description":"Save a durable personal fact or preference. Verify first; keep one fact per memory; say what you saved. Never store secrets, credentials, routine state or procedures (put procedures in skills/). Use dedupe_key to update an existing fact instead of duplicating it.","parameters":{"type":"object","properties":{"subject":{"type":"string","description":"Short title, one line."},"summary":{"type":"string","description":"The fact in one or two sentences."},"body":{"type":"string","description":"Optional details."},"category":{"type":"string","description":"Optional freeform category, e.g. profile, identifier, project."},"source_uris":{"type":"array","items":{"type":"string"},"maxItems":10,"description":"Optional canonical sources."},"dedupe_key":{"type":"string","description":"Stable key for updates, e.g. profile:timezone."}},"required":["subject","summary"],"additionalProperties":false},"sequential":true,"snippet":"Save a memory"}`,
	`{"name":"delete","description":"Forget a memory by id. Soft-deletes by default; force removes it permanently.","parameters":{"type":"object","properties":{"id":{"type":"string"},"force":{"type":"boolean","description":"Remove the file instead of marking it deleted."}},"required":["id"],"additionalProperties":false},"sequential":true,"destructive":true,"snippet":"Forget a memory"}`,
}

type arguments struct {
	Query      string   `json:"query,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	ID         string   `json:"id,omitempty"`
	Topic      string   `json:"topic,omitempty"`
	Subject    string   `json:"subject,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Body       string   `json:"body,omitempty"`
	Category   string   `json:"category,omitempty"`
	SourceURIs []string `json:"source_uris,omitempty"`
	DedupeKey  string   `json:"dedupe_key,omitempty"`
	Force      bool     `json:"force,omitempty"`
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 1 && args[0] == "describe" {
		for _, descriptor := range descriptors {
			fmt.Println(descriptor)
		}
		return
	}
	if len(args) != 2 || args[0] != "run" {
		fail(2, "usage: memoryx describe | memoryx run NAME")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput+1))
	if err != nil {
		fail(1, err.Error())
	}
	if len(input) > maxInput {
		fail(2, "input exceeds 64 KiB")
	}
	var value arguments
	decode(input, &value)
	store, err := openStore()
	if err != nil {
		fail(1, err.Error())
	}
	switch args[1] {
	case "search":
		items, err := store.Search(value.Query, value.Limit)
		if err != nil {
			fail(1, err.Error())
		}
		fmt.Print(formatList(items))
	case "get":
		if value.ID == "" {
			fail(2, "invalid input: id is required")
		}
		item, err := store.Get(value.ID)
		if err != nil {
			fail(1, err.Error())
		}
		fmt.Print(formatOne(item))
	case "verify":
		if strings.TrimSpace(value.Topic) == "" {
			fail(2, "invalid input: topic is required")
		}
		items, err := store.Search(value.Topic, 5)
		if err != nil {
			fail(1, err.Error())
		}
		if len(items) == 0 {
			fmt.Print("No related memories. Safe to add.")
		} else {
			fmt.Print("Existing memories:\n" + formatList(items))
		}
	case "upsert":
		item, created, err := store.Upsert(memory.Memory{
			Subject:    value.Subject,
			Summary:    value.Summary,
			Body:       value.Body,
			Category:   value.Category,
			SourceURIs: value.SourceURIs,
			DedupeKey:  value.DedupeKey,
		})
		if err != nil {
			fail(1, err.Error())
		}
		if created {
			fmt.Printf("saved `%s` (created)\n", item.ID)
		} else {
			fmt.Printf("saved `%s` (updated)\n", item.ID)
		}
	case "delete":
		if value.ID == "" {
			fail(2, "invalid input: id is required")
		}
		if err := store.Delete(value.ID, value.Force); err != nil {
			fail(1, err.Error())
		}
		if value.Force {
			fmt.Printf("deleted `%s`\n", value.ID)
		} else {
			fmt.Printf("forgot `%s`\n", value.ID)
		}
	default:
		fail(2, "unknown tool: "+args[1])
	}
}

func openStore() (*memory.Store, error) {
	root := botdir.EnvRoot()
	if root == "" {
		root = "."
	}
	paths := botdir.Resolve(root)
	return memory.New(filepath.Join(paths.State, "memory")), nil
}

func decode(input []byte, value any) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		fail(2, "invalid input: "+err.Error())
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		fail(2, "invalid input: expected one JSON object")
	}
}

func formatList(items []memory.Memory) string {
	if len(items) == 0 {
		return "No memories."
	}
	var result strings.Builder
	for _, item := range items {
		fmt.Fprintf(&result, "- `%s`", item.ID)
		if item.Category != "" {
			fmt.Fprintf(&result, " [%s]", item.Category)
		}
		fmt.Fprintf(&result, ": %s — %s\n", item.Subject, item.Summary)
	}
	return result.String()
}

func formatOne(item memory.Memory) string {
	var result strings.Builder
	fmt.Fprintf(&result, "# %s\n\n", item.Subject)
	fmt.Fprintf(&result, "id: `%s`\n", item.ID)
	if item.Category != "" {
		fmt.Fprintf(&result, "category: %s\n", item.Category)
	}
	fmt.Fprintf(&result, "updated: %s\n\n", item.UpdatedAt.Format("2006-01-02"))
	fmt.Fprintf(&result, "%s\n", item.Summary)
	if item.Body != "" {
		fmt.Fprintf(&result, "\n%s\n", item.Body)
	}
	if len(item.SourceURIs) > 0 {
		fmt.Fprintf(&result, "\nSources:\n")
		for _, uri := range item.SourceURIs {
			fmt.Fprintf(&result, "- %s\n", uri)
		}
	}
	return result.String()
}

func fail(status int, message string) {
	fmt.Fprintln(os.Stderr, "memoryx:", message)
	os.Exit(status)
}
