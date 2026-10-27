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
)

const maxInput = 16 * 1024

var descriptors = []string{
	`{"name":"skills","description":"List available skills with their names and descriptions.","parameters":{"type":"object","properties":{},"additionalProperties":false},"snippet":"List installed skills"}`,
	`{"name":"skill","description":"Read one skill's content by name. Use the skills tool to list names first. When a skill references a relative path, resolve it against the skill's directory.","parameters":{"type":"object","properties":{"name":{"type":"string","description":"Skill name"}},"required":["name"],"additionalProperties":false},"snippet":"Read an installed skill"}`,
}

type arguments struct {
	Name string `json:"name"`
}

type skill struct {
	name        string
	description string
	content     string
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
	if root := os.Getenv("BOT_ROOT"); root != "" {
		if skillsDir := filepath.Join(root, "skills"); pathExists(skillsDir) {
			open, err := os.OpenRoot(skillsDir)
			if err != nil {
				return nil, err
			}
			return &store{roots: []*os.Root{open}}, nil
		}
	}
	projectPaths := []string{filepath.Join("skills"), filepath.Join(".agents", "skills")}
	var paths []string
	for _, path := range projectPaths {
		if pathExists(path) {
			paths = append(paths, path)
			break
		}
	}
	if home, err := os.UserHomeDir(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: resolve home directory: %v\n", err)
	} else {
		paths = append(paths, filepath.Join(home, ".agents", "skills"))
	}
	skills := &store{}
	seen := make(map[string]struct{})
	for _, path := range paths {
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
	items := make([]skill, 0)
	seen := make(map[string]struct{})
	for _, skillRoot := range skills.roots {
		root, err := skillRoot.Open(".")
		if err != nil {
			return "", err
		}
		entries, err := root.ReadDir(-1)
		_ = root.Close()
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if !entry.IsDir() || validateName(entry.Name()) != nil {
				continue
			}
			if _, exists := seen[entry.Name()]; exists {
				continue
			}
			item, err := load(skillRoot, entry.Name())
			if err != nil || strings.TrimSpace(item.description) == "" {
				continue
			}
			seen[item.name] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].name < items[j].name
	})
	if len(items) == 0 {
		return "No skills installed.", nil
	}
	var result strings.Builder
	for _, item := range items {
		fmt.Fprintf(&result, "- %s: %s\n", item.name, item.description)
	}
	return result.String(), nil
}

func (skills *store) read(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	for _, root := range skills.roots {
		item, err := load(root, name)
		if err != nil || strings.TrimSpace(item.description) == "" {
			continue
		}
		if item.content == "" {
			return "Skill is empty.", nil
		}
		return item.content, nil
	}
	return "", fmt.Errorf("skill not found: %s", name)
}

func load(root *os.Root, name string) (skill, error) {
	content, err := root.ReadFile(filepath.Join(name, "SKILL.md"))
	if err != nil {
		return skill{}, err
	}
	description, body := parse(string(content))
	return skill{name: name, description: description, content: body}, nil
}

func parse(content string) (string, string) {
	if strings.HasPrefix(content, "---") {
		rest := strings.TrimPrefix(content, "---")
		if end := strings.Index(rest, "\n---"); end >= 0 {
			frontmatter := rest[:end]
			body := strings.TrimLeft(rest[end+4:], "\n")
			description := ""
			for _, line := range strings.Split(frontmatter, "\n") {
				key, value, found := strings.Cut(line, ":")
				if found && strings.TrimSpace(key) == "description" {
					description = strings.TrimSpace(value)
				}
			}
			if description == "" {
				description = firstLine(body)
			}
			return description, body
		}
	}
	return firstLine(content), content
}

func firstLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return errors.New("invalid skill name")
	}
	if name[0] == '-' || name[len(name)-1] == '-' || strings.Contains(name, "--") {
		return errors.New("invalid skill name")
	}
	for _, character := range name {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				if character != '-' {
					return errors.New("invalid skill name")
				}
			}
		}
	}
	return nil
}

func fail(status int, message string) {
	fmt.Fprintln(os.Stderr, "skillx:", message)
	os.Exit(status)
}
