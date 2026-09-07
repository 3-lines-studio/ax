package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type userCommand struct {
	name        string
	description string
	content     string
}

func loadUserCommands(axRoot string) []userCommand {
	dir := filepath.Join(axRoot, "commands")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: list user commands %s: %v\n", dir, err)
		}
		return nil
	}
	var out []userCommand
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: read user command %s: %v\n", filepath.Join(dir, entry.Name()), err)
			continue
		}
		description, body := parseFrontmatter(string(content))
		out = append(out, userCommand{name: name, description: description, content: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func expandUserCommand(uc userCommand, rest string) string {
	content := uc.content
	if rest == "" {
		return content
	}
	args := parseCommandArgs(rest)
	substituted := substituteArgs(content, args)
	if substituted == content && !strings.Contains(content, "$ARGUMENTS") {
		return content + "\n\n" + rest
	}
	return substituted
}

func parseCommandArgs(s string) []string {
	args := []string{}
	var current strings.Builder
	var quote byte
	for _, r := range s {
		if quote != 0 {
			if byte(r) == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = byte(r)
		case ' ', '\t', '\n':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

func substituteArgs(content string, args []string) string {
	all := strings.Join(args, " ")
	var out strings.Builder
	rest := content
	for {
		start := strings.Index(rest, "$")
		if start < 0 {
			break
		}
		out.WriteString(rest[:start])
		tail := rest[start:]
		token, consumed := parsePlaceholder(tail, args, all)
		out.WriteString(token)
		rest = tail[consumed:]
	}
	out.WriteString(rest)
	return out.String()
}

func parsePlaceholder(tail string, args []string, all string) (string, int) {
	chars := []rune(tail)
	if len(chars) == 0 || chars[0] != '$' {
		return "", 0
	}
	if len(chars) >= 2 && chars[1] == '{' {
		depth := 0
		end := 0
		for i, c := range chars {
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end == 0 {
			return "", 1
		}
		inner := string(chars[2:end])
		replacement, _ := expandBraced(inner, args, all)
		return replacement, end + 1
	}
	if len(chars) >= 2 && chars[1] == '@' {
		return all, 2
	}
	if len(chars) >= 2 && isASCIIDigit(chars[1]) {
		idx := int(chars[1]-'0') - 1
		value := ""
		if idx >= 0 && idx < len(args) {
			value = args[idx]
		}
		return value, 2
	}
	idLen := 0
	for idLen+1 < len(chars) && (isASCIIAlnum(chars[idLen+1]) || chars[idLen+1] == '_') {
		idLen++
	}
	if idLen > 0 {
		ident := string(chars[1 : idLen+1])
		if ident == "ARGUMENTS" {
			return all, idLen + 1
		}
	}
	return "", 1
}

func expandBraced(inner string, args []string, all string) (string, int) {
	if target, def, hasDefault := strings.Cut(inner, ":-"); hasDefault {
		value := ""
		if target == "@" || target == "ARGUMENTS" {
			value = all
		} else if n, err := strconv.Atoi(target); err == nil && n-1 >= 0 && n-1 < len(args) {
			value = args[n-1]
		}
		if value == "" {
			return def, len(inner)
		}
		return value, len(inner)
	}
	if slice, ok := strings.CutPrefix(inner, "@:"); ok {
		parts := strings.Split(slice, ":")
		if n, err := strconv.Atoi(parts[0]); err == nil {
			start := n - 1
			if start < 0 {
				start = 0
			}
			if start > len(args) {
				start = len(args)
			}
			sliced := args[start:]
			chosen := sliced
			if len(parts) > 1 {
				if length, err := strconv.Atoi(parts[1]); err == nil && length < len(sliced) {
					chosen = sliced[:length]
				}
			}
			return strings.Join(chosen, " "), len(inner)
		}
	}
	return "", len(inner)
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

func isASCIIAlnum(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
