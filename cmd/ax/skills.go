package main

import "strings"

func parseFrontmatter(text string) (string, string) {
	rest, ok := strings.CutPrefix(text, "---")
	if ok {
		if end := strings.Index(rest, "\n---"); end >= 0 {
			fm := rest[:end]
			body := strings.TrimLeft(rest[end+4:], "\n")
			body = strings.TrimLeft(body, " \t")
			description := ""
			for _, line := range strings.Split(fm, "\n") {
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
	return firstLine(text), text
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
