package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/3-lines-studio/ax/internal/botdir"
	"github.com/3-lines-studio/ax/internal/filetext"
)

const maxInput = 1024 * 1024
const maxOutput = 16 * 1024

var descriptors = []string{
	`{"name":"read","description":"Read the contents of a file. Output is truncated to 16KB. Use offset/limit for large files. When you need the full file, continue with the suggested offset.","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"integer","minimum":1,"description":"Line number to start reading from (1-indexed)"},"limit":{"type":"integer","minimum":0,"description":"Maximum number of lines to read"}},"required":["path"],"additionalProperties":false},"read_only":true,"snippet":"Read file contents"}`,
	`{"name":"write","description":"Write content to a file. Creates the file if it does not exist, overwrites it if it does, and creates parent directories.","parameters":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false},"sequential":true,"destructive":true,"snippet":"Write a file"}`,
	`{"name":"edit","description":"Edit a file using exact text replacements. Read the file first. Every oldText must match one unique, non-overlapping region.","parameters":{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","minItems":1,"items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["oldText","newText"],"additionalProperties":false}}},"required":["path","edits"],"additionalProperties":false},"sequential":true,"destructive":true,"snippet":"Edit a file with exact replacements"}`,
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
	return filetext.Read(content, args.Offset, args.Limit)
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
	edits := make([]filetext.Edit, 0, len(args.Edits))
	for _, item := range args.Edits {
		edits = append(edits, filetext.Edit{OldText: item.OldText, NewText: item.NewText})
	}
	updated, message, err := filetext.Apply(content, args.Path, edits)
	if err != nil {
		return "", err
	}
	if err := files.atomicWrite(path, updated); err != nil {
		return "", err
	}
	return message, nil
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

func fail(status int, message string) {
	fmt.Fprintln(os.Stderr, "fsx:", message)
	os.Exit(status)
}
