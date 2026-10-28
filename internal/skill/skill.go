// Package skill reads Agent Skills directories: one folder per skill, with a
// SKILL.md whose optional YAML frontmatter carries the description. It is the
// shared reader behind the skillx provider and the webi Skills view.
package skill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileName is the file that defines a skill.
const FileName = "SKILL.md"

// Skill is one skill directory.
type Skill struct {
	Name        string
	Description string
	Body        string
	// Error marks a directory the agent would skip: an invalid name, a missing
	// SKILL.md or no description. The UI shows it; skillx filters it out.
	Error string
}

// Parse returns the description and body of a SKILL.md. A frontmatter
// description wins; otherwise the first non-empty line is the description.
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

// ValidName reports whether name is a skill directory the agent can address:
// lowercase letters, digits and single dashes, up to 64 characters.
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

// Roots returns the skill directories a bot sees, in priority order. When the
// bot has skills/, that directory is the only one; otherwise the workspace's
// project directories and the user's ~/.agents/skills apply. Paths may not
// exist; callers skip what they cannot open.
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

// List reads every skill directory under root, sorted by name. A missing root
// is an empty list. Entries the agent would ignore carry Error instead of
// being dropped, so a broken skill stays visible.
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

// Read loads one skill by name. The name is validated first, and root keeps a
// symlink from escaping the skills directory.
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
