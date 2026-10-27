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
	"sync/atomic"

	"github.com/3-lines-studio/ax/internal/botdir"
)

const maxInput = 1024 * 1024
const maxOutput = 16 * 1024

var descriptors = []string{
	`{"name":"read","description":"Read the contents of a file. Output is truncated to 16KB. Use offset/limit for large files. When you need the full file, continue with the suggested offset.","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"integer","minimum":1,"description":"Line number to start reading from (1-indexed)"},"limit":{"type":"integer","minimum":0,"description":"Maximum number of lines to read"}},"required":["path"],"additionalProperties":false},"snippet":"Read file contents"}`,
	`{"name":"write","description":"Write content to a file. Creates the file if it does not exist, overwrites it if it does, and creates parent directories.","parameters":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false},"sequential":true,"snippet":"Write a file"}`,
	`{"name":"edit","description":"Edit a file using exact text replacements. Every oldText must match one unique, non-overlapping region.","parameters":{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","minItems":1,"items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["oldText","newText"],"additionalProperties":false}}},"required":["path","edits"],"additionalProperties":false},"sequential":true,"snippet":"Edit a file with exact replacements"}`,
}

type readArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type edit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type editArgs struct {
	Path  string `json:"path"`
	Edits []edit `json:"edits"`
}

type filesystem struct {
	root      *os.Root
	workspace string
}

var temporaryID atomic.Uint64

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
		fail(2, "usage: fsx describe | fsx run NAME")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput+1))
	if err != nil {
		fail(1, err.Error())
	}
	if len(input) > maxInput {
		fail(2, "input exceeds 1 MiB")
	}
	files, err := openFilesystem()
	if err != nil {
		fail(1, err.Error())
	}
	defer func() { _ = files.root.Close() }()
	var result string
	switch args[1] {
	case "read":
		var value readArgs
		decode(input, &value)
		result, err = files.read(value)
	case "write":
		var value writeArgs
		decode(input, &value)
		result, err = files.write(value)
	case "edit":
		var value editArgs
		decode(input, &value)
		result, err = files.edit(value)
	default:
		fail(2, "unknown tool: "+args[1])
	}
	if err != nil {
		fail(1, err.Error())
	}
	fmt.Print(result)
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

func openFilesystem() (*filesystem, error) {
	workspace, err := filepath.Abs(botdir.Workspace())
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	return &filesystem{root: root, workspace: workspace}, nil
}

func (files *filesystem) path(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if !filepath.IsAbs(path) {
		return path, nil
	}
	relative, err := filepath.Rel(files.workspace, path)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the workspace")
	}
	return relative, nil
}

func (files *filesystem) read(args readArgs) (string, error) {
	path, err := files.path(args.Path)
	if err != nil {
		return "", err
	}
	content, err := files.root.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := strings.ToValidUTF8(string(content), "�")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = sanitize(text)
	lines := strings.Split(text, "\n")
	if text == "" || strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	start := 0
	if args.Offset != nil {
		if *args.Offset < 1 {
			return "", errors.New("offset must be positive")
		}
		start = *args.Offset - 1
		if start >= len(lines) {
			return "", fmt.Errorf("offset %d is beyond end of file (%d lines total)", *args.Offset, len(lines))
		}
	}
	end := len(lines)
	if args.Limit != nil {
		if *args.Limit < 0 {
			return "", errors.New("limit must not be negative")
		}
		end = min(start+*args.Limit, end)
	}
	selected := strings.Join(lines[start:end], "\n")
	if len(selected) <= maxOutput {
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
		if size+added > maxOutput {
			break
		}
		shown = append(shown, line)
		size += added
	}
	if len(shown) == 0 {
		return fmt.Sprintf("[Line %d is %d bytes, exceeds the %d limit.]", start+1, len(lines[start]), maxOutput), nil
	}
	last := start + len(shown)
	return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d (%d byte limit). Use offset=%d to continue.]", strings.Join(shown, "\n"), start+1, last, len(lines), maxOutput, last+1), nil
}

func (files *filesystem) write(args writeArgs) (string, error) {
	path, err := files.path(args.Path)
	if err != nil {
		return "", err
	}
	if err := files.atomicWrite(path, []byte(args.Content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", args.Path, len(args.Content)), nil
}

func (files *filesystem) edit(args editArgs) (string, error) {
	path, err := files.path(args.Path)
	if err != nil {
		return "", err
	}
	if len(args.Edits) == 0 {
		return "", errors.New("edits must not be empty")
	}
	content, err := files.root.ReadFile(path)
	if err != nil {
		return "", err
	}
	original := string(content)
	type replacement struct {
		start int
		end   int
		text  string
	}
	replacements := make([]replacement, 0, len(args.Edits))
	for index, item := range args.Edits {
		if item.OldText == "" {
			return "", fmt.Errorf("edits[%d].oldText must not be empty", index)
		}
		start, end, count := locate(original, item.OldText)
		if count == 0 {
			return "", fmt.Errorf("could not find edits[%d] in %s", index, args.Path)
		}
		if count != 1 {
			return "", fmt.Errorf("found %d occurrences of edits[%d] in %s", count, index, args.Path)
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
			return "", errors.New("edits overlap")
		}
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		item := replacements[i]
		original = original[:item.start] + item.text + original[item.end:]
	}
	if err := files.atomicWrite(path, []byte(original)); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d replacements)", args.Path, len(args.Edits)), nil
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

func (files *filesystem) atomicWrite(path string, content []byte) error {
	parent := filepath.Dir(path)
	if err := files.root.MkdirAll(parent, 0755); err != nil {
		return err
	}
	temporary := filepath.Join(parent, fmt.Sprintf(".fsx-%d-%d", os.Getpid(), temporaryID.Add(1)))
	file, err := files.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = files.root.Remove(temporary) }()
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return files.root.Rename(temporary, path)
}

func sanitize(value string) string {
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

func fail(status int, message string) {
	fmt.Fprintln(os.Stderr, "fsx:", message)
	os.Exit(status)
}
