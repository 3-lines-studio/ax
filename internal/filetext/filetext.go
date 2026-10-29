package filetext

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const MaxOutput = 16 * 1024

type Edit struct {
	OldText string
	NewText string
}

func Read(content []byte, offset, limit *int) (string, error) {
	text := strings.ToValidUTF8(string(content), "�")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = Sanitize(text)
	lines := strings.Split(text, "\n")
	if text == "" || strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	start := 0
	if offset != nil {
		if *offset < 1 {
			return "", errors.New("offset must be positive")
		}
		start = *offset - 1
		if start >= len(lines) {
			return "", fmt.Errorf("offset %d is beyond end of file (%d lines total)", *offset, len(lines))
		}
	}
	end := len(lines)
	if limit != nil {
		if *limit < 0 {
			return "", errors.New("limit must not be negative")
		}
		end = min(start+*limit, end)
	}
	selected := strings.Join(lines[start:end], "\n")
	if len(selected) <= MaxOutput {
		if end < len(lines) {
			selected += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", len(lines)-end, end+1)
		}
		if selected == "" {
			return "File is empty.", nil
		}
		return selected, nil
	}
	shown := make([]string, 0)
	size := 0
	for _, line := range lines[start:end] {
		added := len(line)
		if len(shown) > 0 {
			added++
		}
		if size+added > MaxOutput {
			break
		}
		shown = append(shown, line)
		size += added
	}
	if len(shown) == 0 {
		return fmt.Sprintf("[Line %d is %d bytes, exceeds the %d limit.]", start+1, len(lines[start]), MaxOutput), nil
	}
	last := start + len(shown)
	return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d (%d byte limit). Use offset=%d to continue.]", strings.Join(shown, "\n"), start+1, last, len(lines), MaxOutput, last+1), nil
}

func Apply(content []byte, path string, edits []Edit) ([]byte, string, error) {
	if len(edits) == 0 {
		return nil, "", errors.New("edits must not be empty")
	}
	original := string(content)
	type replacement struct {
		start int
		end   int
		text  string
	}
	replacements := make([]replacement, 0, len(edits))
	for index, item := range edits {
		if item.OldText == "" {
			return nil, "", fmt.Errorf("edits[%d].oldText must not be empty", index)
		}
		start, end, count := locate(original, item.OldText)
		if count == 0 {
			return nil, "", fmt.Errorf("could not find edits[%d] in %s", index, path)
		}
		if count != 1 {
			return nil, "", fmt.Errorf("found %d occurrences of edits[%d] in %s", count, index, path)
		}
		newText := item.NewText
		if strings.Contains(original[start:end], "\r\n") {
			newText = strings.ReplaceAll(strings.ReplaceAll(newText, "\r\n", "\n"), "\n", "\r\n")
		}
		replacements = append(replacements, replacement{start, end, newText})
	}
	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})
	for i := 1; i < len(replacements); i++ {
		if replacements[i].start < replacements[i-1].end {
			return nil, "", errors.New("edits overlap")
		}
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		item := replacements[i]
		original = original[:item.start] + item.text + original[item.end:]
	}
	return []byte(original), fmt.Sprintf("edited %s (%d replacements)", path, len(edits)), nil
}

func locate(content, oldText string) (int, int, int) {
	count := strings.Count(content, oldText)
	if count > 0 {
		start := strings.Index(content, oldText)
		return start, start + len(oldText), count
	}
	normalized, offsets := normalizeWithOffsets(content)
	oldText = strings.ReplaceAll(strings.ReplaceAll(oldText, "\r\n", "\n"), "\r", "\n")
	count = strings.Count(normalized, oldText)
	if count == 0 {
		return 0, 0, 0
	}
	start := strings.Index(normalized, oldText)
	return offsets[start], offsets[start+len(oldText)], count
}

func normalizeWithOffsets(content string) (string, []int) {
	input := []byte(content)
	output := make([]byte, 0, len(input))
	offsets := make([]int, 0, len(input)+1)
	for index := 0; index < len(input); {
		offsets = append(offsets, index)
		if input[index] == '\r' {
			output = append(output, '\n')
			if index+1 < len(input) && input[index+1] == '\n' {
				index += 2
			} else {
				index++
			}
			continue
		}
		output = append(output, input[index])
		index++
	}
	offsets = append(offsets, len(input))
	return string(output), offsets
}

func Sanitize(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\t' || character == '\n' || character == '\r' {
			return character
		}
		if character <= 0x1f || character >= 0xfff9 && character <= 0xfffb {
			return -1
		}
		return character
	}, value)
}
