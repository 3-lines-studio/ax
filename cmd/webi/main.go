// Command webi is the browser transport for ax. It serves a static UI, keeps
// channels (bot directories) and threads (ax sessions), and streams one ax
// process per message, exactly like slaxi does for Slack.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
	"github.com/3-lines-studio/ax/internal/botdir"
	"github.com/3-lines-studio/ax/internal/routine"
)

func main() {
	listen := flag.String("listen", "", "address to serve the UI on (default 127.0.0.1:8377)")
	stateDir := flag.String("state", "", "state directory (default <BOT_DATA>/state/webi)")
	botRoot := flag.String("C", "", "bot directory (default: the per-user app directory)")
	axPath := flag.String("ax", "ax", "path to the ax binary")
	baseURL := flag.String("base", "", "override the model endpoint for every run")
	model := flag.String("model", "", "override the model for every run")
	selftest := flag.Bool("selftest", false, "check the bot, state and ax binary, then exit")
	open := flag.Bool("open", true, "open the token URL in a browser when run in a terminal")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: webi [flags] [setup]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "  setup   install a systemd user unit, start the UI and open it")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	switch flag.Arg(0) {
	case "setup":
		if err := runSetup(setupOptions{Root: *botRoot, State: *stateDir, Listen: *listen, AXPath: *axPath, Open: *open, Wait: 10 * time.Second}); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	case "":
	default:
		fmt.Fprintf(os.Stderr, "webi: unknown command %q\n", flag.Arg(0))
		os.Exit(2)
	}

	listenAddr := *listen
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8377"
	}
	root, fresh, err := resolveRoot(*botRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if fresh {
		if err := writeStarter(root); err != nil {
			slog.Warn("webi: starter bot", "err", err)
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

	token, tokenPath, err := loadToken(state)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if alreadyServes(listenAddr, token) {
		host, port := splitListen(listenAddr)
		printURLs(host, port, token, *open, tokenPath)
		return
	}
	if _, err := exec.LookPath(*axPath); err != nil {
		fmt.Fprintf(os.Stderr, "webi: cannot find ax (%s); install it with `install.sh ax` or pass -ax\n", *axPath)
		os.Exit(1)
	}
	srv.token = token

	// A promotion restarts webi; the key the host kept saves the PIN.
	srv.adoptVaultKey()

	// A run that outlived the last process is picked up here, not by the user.
	srv.StartResumeLoop(resumeInterval)

	server := &http.Server{
		Handler:           srv.auth(srv.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	slog.Info("webi listening", "addr", "http://"+listener.Addr().String(), "bot", paths.Root, "state", state, "ax", *axPath)
	if tokenPath != "" {
		slog.Info("webi: first visit needs ?token= from", "file", tokenPath)
	}
	announce(listener, token, *open)
	schedCtx, stopScheduler := context.WithCancel(context.Background())
	defer stopScheduler()
	go routine.NewScheduler(paths).Run(schedCtx)
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
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
