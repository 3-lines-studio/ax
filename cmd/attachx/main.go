package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/3-lines-studio/ax/internal/botdir"
)

const maxSize = 25 * 1024 * 1024

type artifact struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "describe" {
		fmt.Println(`{"name":"attach_file","description":"Attach a workspace file to the current conversation","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Path to the workspace file"}},"required":["path"]}}`)
		return
	}
	if len(args) != 2 || args[0] != "run" || args[1] != "attach_file" {
		fail(2, "usage: attachx describe | attachx run attach_file")
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&input); err != nil {
		fail(2, "invalid arguments")
	}
	item, err := attach(input.Path)
	if err != nil {
		fail(1, err.Error())
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]artifact{"artifact": item}); err != nil {
		fail(1, err.Error())
	}
}

func attach(path string) (artifact, error) {
	root := botdir.EnvRoot()
	if root == "" {
		return artifact{}, errors.New("BOT_ROOT or BOT_DATA is required")
	}
	workspace, err := filepath.EvalSymlinks(filepath.Join(root, "workspace"))
	if err != nil {
		return artifact{}, errors.New("BOT_ROOT/workspace or BOT_DATA/workspace is required")
	}
	source, err := filepath.EvalSymlinks(path)
	if err != nil {
		return artifact{}, err
	}
	relative, err := filepath.Rel(workspace, source)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return artifact{}, errors.New("file is outside the workspace")
	}
	file, err := os.Open(source)
	if err != nil {
		return artifact{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return artifact{}, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxSize {
		return artifact{}, errors.New("file must be a non-empty regular file no larger than 25 MB")
	}
	directory := os.Getenv("AX_ARTIFACT_DIR")
	if directory == "" {
		return artifact{}, errors.New("AX_ARTIFACT_DIR is required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return artifact{}, err
	}
	id, err := randomID()
	if err != nil {
		return artifact{}, err
	}
	name := filepath.Base(source)
	destination := filepath.Join(directory, id+"-"+name)
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return artifact{}, err
	}
	_, copyErr := io.Copy(output, io.LimitReader(file, maxSize+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return artifact{}, errors.New("copy artifact")
	}
	buffer := make([]byte, 512)
	read, _ := file.ReadAt(buffer, 0)
	mediaType := http.DetectContentType(buffer[:read])
	if extensionType := mime.TypeByExtension(filepath.Ext(name)); extensionType != "" {
		mediaType = extensionType
	}
	return artifact{ID: id, Name: name, MediaType: mediaType, Size: info.Size()}, nil
}

func randomID() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func fail(code int, message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(code)
}
