package skill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const FileName = "SKILL.md"

type Skill struct {
	Name        string
	Description string
	Body        string
	Error       string
}

func Parse(content string) (description, body string) {
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

func ValidName(name string) error {
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

func Roots(botRoot, workspace, home string) []string {
	if botRoot != "" {
		if dir := filepath.Join(botRoot, "skills"); exists(dir) {
			return []string{dir}
		}
	}
	var roots []string
	for _, candidate := range []string{
		filepath.Join(workspace, "skills"),
		filepath.Join(workspace, ".agents", "skills"),
	} {
		if exists(candidate) {
			roots = append(roots, candidate)
			break
		}
	}
	if home != "" {
		roots = append(roots, filepath.Join(home, ".agents", "skills"))
	}
	return roots
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func List(root *os.Root) ([]Skill, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	items := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item := Skill{Name: entry.Name()}
		if err := ValidName(item.Name); err != nil {
			item.Error = "invalid name: use lowercase letters, digits and dashes"
			items = append(items, item)
			continue
		}
		read, err := Read(root, item.Name)
		if err != nil {
			item.Error = "cannot read " + FileName
			if errors.Is(err, fs.ErrNotExist) {
				item.Error = "missing " + FileName
			}
			items = append(items, item)
			continue
		}
		items = append(items, read)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func Read(root *os.Root, name string) (Skill, error) {
	if err := ValidName(name); err != nil {
		return Skill{}, err
	}
	content, err := root.ReadFile(filepath.Join(name, FileName))
	if err != nil {
		return Skill{}, err
	}
	description, body := Parse(string(content))
	item := Skill{Name: name, Description: description, Body: body}
	if strings.TrimSpace(description) == "" {
		item.Error = "no description: the agent ignores it"
	}
	return item, nil
}
