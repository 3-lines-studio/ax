// Command webi is the browser transport for ax. It serves a static UI, keeps
// channels (bot directories) and threads (ax sessions), and streams one ax
// process per message, exactly like slaxi does for Slack.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
	"github.com/3-lines-studio/ax/internal/botdir"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8377", "address to serve the UI on")
	stateDir := flag.String("state", "", "state directory (default <BOT_DATA>/state/webi)")
	botRoot := flag.String("C", "", "bot directory (default: current directory)")
	axPath := flag.String("ax", "ax", "path to the ax binary")
	baseURL := flag.String("base", "", "override the model endpoint for every run")
	model := flag.String("model", "", "override the model for every run")
	selftest := flag.Bool("selftest", false, "check the bot, state and ax binary, then exit")
	flag.Parse()

	root := *botRoot
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: working directory")
			os.Exit(1)
		}
	}
	paths := botdir.Resolve(root)
	state := *stateDir
	if state == "" {
		state = filepath.Join(paths.DataRoot, "state", "webi")
	}
	if *selftest {
		os.Exit(runSelftest(root, state, *axPath, os.Stdout, os.Stderr))
	}
	store, err := NewStore(state)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	// The first channel is the bot root itself, so a fresh install works.
	channels := store.Channels()
	if len(channels) == 1 && channels[0].Workdir == state {
		channels[0].Workdir = paths.Root
		_ = store.PutChannel(channels[0])
	}

	srv := NewServer(store, axrun.Config{
		AXPath:  *axPath,
		BotRoot: paths.Root,
		BaseURL: *baseURL,
		Model:   *model,
	}, state, staticHandler(), buildID())

	// A run that outlived the last process is picked up here, not by the user.
	srv.StartResumeLoop(resumeInterval)

	server := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("webi listening", "addr", "http://"+*listen, "bot", paths.Root, "state", state, "ax", *axPath)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// buildID labels the process for /healthz and the UI. The host that supervises
// a build sets AX_BUILD (boti serve does); a plain checkout reports "dev".
func buildID() string {
	if v := strings.TrimSpace(os.Getenv("AX_BUILD")); v != "" {
		return v
	}
	return "dev"
}

// readSession projects an ax session log into the events the UI renders. It
// knows only what the UI needs: who said what, and where the context was
// compacted.
func readSession(path string) ([]map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	events := []map[string]any{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
			Upto    int             `json:"upto"`
			At      int64           `json:"at,omitempty"`
			Resume  bool            `json:"resume,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		switch entry.Type {
		case "message", "":
			var msg axrun.Message
			if len(entry.Message) == 0 || json.Unmarshal(entry.Message, &msg) != nil {
				continue
			}
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			if entry.Resume && msg.Role == "user" {
				// The resume turn is ax talking to the model, not the user.
				events = append(events, map[string]any{"t": "resume", "ts": eventTime(entry.At)})
				continue
			}
			events = append(events, map[string]any{
				"t": msg.Role, "text": msg.Content, "ts": eventTime(entry.At),
			})
		case "compaction":
			events = append(events, map[string]any{"t": "compaction", "upto": entry.Upto, "ts": eventTime(entry.At)})
		}
	}
	return events, scanner.Err()
}

func eventTime(millis int64) time.Time {
	if millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}
