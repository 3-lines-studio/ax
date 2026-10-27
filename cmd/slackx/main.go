package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/slack-go/slack"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 1 && args[0] == "describe" {
		fmt.Println(`{"name":"upload_to_slack","description":"Upload a local file to the current Slack thread","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Path to the local file"}},"required":["path"]}}`)
		return
	}
	if len(args) != 2 || args[0] != "run" || args[1] != "upload_to_slack" {
		fail(2, "usage: slackx describe | slackx run upload_to_slack")
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&input); err != nil {
		fail(2, "invalid arguments: "+err.Error())
	}
	info, err := validateFile(input.Path)
	if err != nil {
		fail(2, err.Error())
	}
	token := os.Getenv("SLACK_BOT_TOKEN")
	channel := os.Getenv("AX_SLACK_CHANNEL")
	thread := os.Getenv("AX_SLACK_THREAD")
	if token == "" || channel == "" {
		fail(2, "SLACK_BOT_TOKEN and AX_SLACK_CHANNEL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	file, err := slack.New(token).UploadFileV2Context(ctx, slack.UploadFileV2Parameters{
		File:            input.Path,
		FileSize:        int(info.Size()),
		Channel:         channel,
		ThreadTimestamp: thread,
		Filename:        filepath.Base(input.Path),
	})
	if err != nil {
		fail(1, err.Error())
	}
	fmt.Printf("Uploaded %s to Slack as %s\n", input.Path, file.ID)
}

func validateFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("file must be a non-empty regular file")
	}
	return info, nil
}

func fail(code int, message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(code)
}
