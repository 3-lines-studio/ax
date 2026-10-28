package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3-lines-studio/ax/internal/skill"
)

const maxInput = 16 * 1024

var descriptors = []string{
	`{"name":"skills","description":"List available skills with their names and descriptions.","parameters":{"type":"object","properties":{},"additionalProperties":false},"read_only":true,"snippet":"List installed skills"}`,
	`{"name":"skill","description":"Read one skill's content by name. Use the skills tool to list names first. When a skill references a relative path, resolve it against the skill's directory.","parameters":{"type":"object","properties":{"name":{"type":"string","description":"Skill name"}},"required":["name"],"additionalProperties":false},"read_only":true,"snippet":"Read an installed skill"}`,
}

type arguments struct {
	Name string `json:"name"`
}

type store struct {
	roots []*os.Root
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
		fail(2, "usage: skillx describe | skillx run NAME")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput+1))
	if err != nil {
		fail(1, err.Error())
	}
	if len(input) > maxInput {
		fail(2, "input exceeds 16 KiB")
	}
	var value arguments
	decode(input, &value)
	skills, err := openStore()
	if err != nil {
		fail(1, err.Error())
	}
	defer skills.close()
	switch args[1] {
	case "skills":
		if value.Name != "" {
			fail(2, "invalid input: name is not accepted")
		}
		result, err := skills.list()
		if err != nil {
			fail(1, err.Error())
		}
		fmt.Print(result)
	case "skill":
		if value.Name == "" {
			fail(2, "invalid input: name is required")
		}
		result, err := skills.read(value.Name)
		if err != nil {
			fail(1, err.Error())
		}
		fmt.Print(result)
	default:
		fail(2, "unknown tool: "+args[1])
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func openStore() (*store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: resolve home directory: %v\n", err)
		home = ""
	}
	skills := &store{}
	seen := make(map[string]struct{})
	for _, path := range skill.Roots(os.Getenv("BOT_ROOT"), ".", home) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			skills.close()
			return nil, err
		}
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		root, err := os.OpenRoot(absolute)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			skills.close()
			return nil, err
		}
		skills.roots = append(skills.roots, root)
	}
	return skills, nil
}

func (skills *store) close() {
	for _, root := range skills.roots {
		_ = root.Close()
	}
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

func (skills *store) list() (string, error) {
	items := make([]skill.Skill, 0)
	seen := make(map[string]struct{})
	for _, root := range skills.roots {
		found, err := skill.List(root)
		if err != nil {
			return "", err
		}
		for _, item := range found {
			if item.Error != "" {
				continue
			}
			if _, exists := seen[item.Name]; exists {
				continue
			}
			seen[item.Name] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if len(items) == 0 {
		return "No skills installed.", nil
	}
	var result strings.Builder
	for _, item := range items {
		fmt.Fprintf(&result, "- %s: %s\n", item.Name, item.Description)
	}
	return result.String(), nil
}

func (skills *store) read(name string) (string, error) {
	if err := skill.ValidName(name); err != nil {
		return "", err
	}
	for _, root := range skills.roots {
		item, err := skill.Read(root, name)
		if err != nil || item.Error != "" {
			continue
		}
		if item.Body == "" {
			return "Skill is empty.", nil
		}
		return item.Body, nil
	}
	return "", fmt.Errorf("skill not found: %s", name)
}

func fail(status int, message string) {
	fmt.Fprintln(os.Stderr, "skillx:", message)
	os.Exit(status)
}
