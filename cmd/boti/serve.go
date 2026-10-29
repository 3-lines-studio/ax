package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
)

type serveConfig struct {
	Command  []string `toml:"command"`
	Health   string   `toml:"health"`
	Idle     string   `toml:"idle"`
	Selftest []string `toml:"selftest"`
	Repo     string   `toml:"repo"`
	Data     string   `toml:"data"`
}

const defaultServeListen = "127.0.0.1:8377"

func (bc botConfig) serveSettings() serveConfig {
	cfg := bc.Boti.Serve
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"webi", "-listen", defaultServeListen}
		if cfg.Health == "" {
			cfg.Health = "http://" + defaultServeListen + "/healthz"
		}
	}
	return cfg
}

type hostPaths struct {
	root   string
	data   string
	dir    string
	state  string
	log    string
	sock   string
	builds string
}

func newHostPaths(root string) hostPaths {
	return newHostPathsAt(root, botdir.DataRoot(root))
}

func newHostPathsAt(root, data string) hostPaths {
	dir := filepath.Join(data, "state", "host")
	return hostPaths{
		root:   root,
		data:   data,
		dir:    dir,
		state:  filepath.Join(dir, "state.json"),
		log:    filepath.Join(dir, "log"),
		sock:   filepath.Join(dir, "sock"),
		builds: filepath.Join(dir, "builds"),
	}
}

type hostState struct {
	PID       int        `json:"pid"`
	Build     string     `json:"build"`
	Previous  string     `json:"previous,omitempty"`
	Started   time.Time  `json:"started"`
	Restarts  int        `json:"restarts"`
	LastError string     `json:"last_error,omitempty"`
	Pending   string     `json:"pending,omitempty"`
	Child     childState `json:"child"`
}

type childState struct {
	PID     int       `json:"pid,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Healthy bool      `json:"healthy"`
}

type host struct {
	paths hostPaths
	cfg   serveConfig
	env   []string
	log   *os.File

	mu       sync.Mutex
	switchMu sync.Mutex
	vaultMu  sync.Mutex
	vaultKey []byte
	idleWarn sync.Once
	state    hostState
	restart  chan struct{}
	recent   []time.Time
	failed   map[string]bool
	pending  int
	done     chan struct{}

	startTimeout time.Duration
	stopTimeout  time.Duration
	backoff      time.Duration
	crashWindow  time.Duration
	crashLimit   int
	idleInterval time.Duration
	idleTimeout  time.Duration
}

func newHost(root string, bc botConfig) (*host, error) {
	paths := newHostPaths(root)
	if err := os.MkdirAll(paths.builds, 0o755); err != nil {
		return nil, err
	}
	file, err := openHostLog(paths.log)
	if err != nil {
		return nil, err
	}
	h := &host{
		paths:        paths,
		cfg:          bc.serveSettings(),
		env:          withoutEnv(hostEnv(paths, bc), "AX_VAULT_FD", "AX_HOST_SOCK"),
		log:          file,
		restart:      make(chan struct{}, 1),
		failed:       map[string]bool{},
		done:         make(chan struct{}),
		startTimeout: 30 * time.Second,
		stopTimeout:  10 * time.Second,
		backoff:      time.Second,
		crashWindow:  time.Minute,
		crashLimit:   3,
		idleInterval: time.Second,
		idleTimeout:  30 * time.Minute,
	}
	if data, err := os.ReadFile(paths.state); err == nil {
		_ = json.Unmarshal(data, &h.state)
	}
	h.state.Pending = ""
	h.state.PID = os.Getpid()
	h.state.Started = time.Now().UTC()
	h.state.Child = childState{}
	h.save()
	return h, nil
}

func (h *host) close() {
	if h.log != nil {
		_ = h.log.Close()
	}
}

func (h *host) save() {
	h.mu.Lock()
	data, err := json.MarshalIndent(h.state, "", " ")
	h.mu.Unlock()
	if err != nil {
		slog.Error("boti: marshal state", "err", err)
		return
	}
	if err := writeFileAtomic(h.paths.state, data, 0o644); err != nil {
		slog.Error("boti: save state", "err", err)
	}
}

func (h *host) build() (string, string, error) {
	link := filepath.Join(h.paths.builds, "current")
	target, err := os.Readlink(link)
	if err != nil {
		return "dev", "", nil
	}
	id := filepath.Base(target)
	bin := filepath.Join(h.paths.builds, id, "bin")
	if info, err := os.Stat(bin); err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("build %s has no bin directory (%s)", id, bin)
	}
	return id, bin, nil
}

func (h *host) switchBuild(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return fmt.Errorf("invalid build id %q", id)
	}
	temporary := filepath.Join(h.paths.builds, ".current-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Symlink(id, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(h.paths.builds, "current")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (h *host) signalRestart() {
	select {
	case h.restart <- struct{}{}:
	default:
	}
}

func (h *host) hasBuild(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return false
	}
	info, err := os.Stat(filepath.Join(h.paths.builds, id, "bin"))
	return err == nil && info.IsDir()
}

func (h *host) switchTo(id string) error {
	h.switchMu.Lock()
	defer h.switchMu.Unlock()
	h.mu.Lock()
	current := h.state.Build
	if id == current {
		h.state.Pending = ""
		h.mu.Unlock()
		h.save()
		return nil
	}
	h.mu.Unlock()
	if !h.hasBuild(id) {
		return fmt.Errorf("unknown build %q", id)
	}
	if err := h.switchBuild(id); err != nil {
		return err
	}
	h.mu.Lock()
	if current == "dev" {
		current = ""
	}
	h.state.Previous = current
	h.state.Build = id
	h.state.Pending = ""
	h.mu.Unlock()
	h.save()
	h.pruneBuilds(defaultKeep)
	slog.Info("boti: build selected", "build", id, "previous", current)
	return nil
}

func (h *host) arm(id string) {
	h.mu.Lock()
	h.pending++
	epoch := h.pending
	h.state.Pending = id
	h.mu.Unlock()
	h.save()
	go h.waitIdle(epoch, id)
}

func (h *host) waitIdle(epoch int, id string) {
	ticker := time.NewTicker(h.idleInterval)
	defer ticker.Stop()
	deadline := time.Now().Add(h.idleTimeout)
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
		}
		h.mu.Lock()
		stale := h.pending != epoch || h.state.Pending != id
		h.mu.Unlock()
		if stale {
			return
		}
		if time.Now().After(deadline) {
			slog.Warn("boti: idle timeout, activating anyway", "build", id)
			break
		}
		if h.idle() {
			break
		}
	}
	if err := h.switchTo(id); err != nil {
		slog.Error("boti: activate", "build", id, "err", err)
		return
	}
	h.signalRestart()
}

func (h *host) idle() bool {
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(h.cfg.Idle)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		h.idleWarn.Do(func() {
			slog.Warn("boti: idle check unauthorized; point [boti.serve].idle at /readyz", "url", h.cfg.Idle)
		})
		return false
	}
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var state struct {
		Running *int `json:"running"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&state); err != nil {
		return false
	}
	return state.Running != nil && *state.Running == 0
}

type childProc struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
}

func (h *host) spawn(id, bin string) (*childProc, error) {
	command := h.cfg.Command
	if len(command) == 0 {
		return nil, errors.New("serve.command is empty")
	}
	env := h.env
	env = setEnv(env, "AX_BUILD", id)
	env = setEnv(env, "AX_HOST_SOCK", h.paths.sock)
	if bin != "" {
		path := envValue(env, "PATH")
		if path != "" {
			path = bin + string(os.PathListSeparator) + path
		} else {
			path = bin
		}
		env = setEnv(env, "PATH", path)
	}
	name := command[0]
	if !strings.ContainsRune(name, os.PathSeparator) {
		resolved, err := lookPath(name, envValue(env, "PATH"))
		if err != nil {
			return nil, err
		}
		name = resolved
	}
	cmd := exec.Command(name, command[1:]...)
	cmd.Dir = h.paths.root
	cmd.Env = env
	cmd.Stdout = h.log
	cmd.Stderr = h.log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var keyW *os.File
	key := h.copyVaultKey()
	if len(key) == 32 {
		keyR, writeEnd, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		cmd.ExtraFiles = []*os.File{keyR}
		keyW = writeEnd
		env = setEnv(env, "AX_VAULT_FD", "3")
		cmd.Env = env
	}
	if err := cmd.Start(); err != nil {
		if keyW != nil {
			_ = keyW.Close()
		}
		if len(cmd.ExtraFiles) > 0 {
			_ = cmd.ExtraFiles[0].Close()
		}
		return nil, err
	}
	if len(cmd.ExtraFiles) > 0 {
		_ = cmd.ExtraFiles[0].Close()
	}
	if keyW != nil {
		_, _ = keyW.Write(key)
		_ = keyW.Close()
	}
	proc := &childProc{cmd: cmd, done: make(chan struct{})}
	go func() {
		proc.waitErr = cmd.Wait()
		close(proc.done)
	}()

	h.mu.Lock()
	h.state.Build = id
	h.state.Child = childState{PID: cmd.Process.Pid, Started: time.Now().UTC()}
	h.mu.Unlock()
	h.save()
	slog.Info("boti: started", "pid", cmd.Process.Pid, "build", id, "command", strings.Join(command, " "))
	return proc, nil
}

func (h *host) awaitHealthy(ctx context.Context, proc *childProc) bool {
	if h.cfg.Health == "" {
		h.markHealthy()
		return true
	}
	deadline := time.Now().Add(h.startTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return false
		case <-proc.done:
			return false
		default:
		}
		resp, err := client.Get(h.cfg.Health)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				h.markHealthy()
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (h *host) markHealthy() {
	h.mu.Lock()
	h.state.Child.Healthy = true
	h.failed = map[string]bool{}
	pid := h.state.Child.PID
	h.mu.Unlock()
	h.save()
	slog.Info("boti: healthy", "pid", pid)
}

func (h *host) clearChild() {
	h.mu.Lock()
	h.state.Child = childState{}
	h.mu.Unlock()
	h.save()
}

func (h *host) stop(proc *childProc) {
	select {
	case <-proc.done:
		return
	default:
	}
	_ = proc.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-proc.done:
	case <-time.After(h.stopTimeout):
		slog.Warn("boti: child ignored SIGTERM, killing", "pid", proc.cmd.Process.Pid)
		_ = proc.cmd.Process.Kill()
		<-proc.done
	}
	h.clearChild()
}

func (h *host) crashed(err error, reason string) {
	if err != nil {
		reason = fmt.Sprintf("%s: %v", reason, err)
	}
	h.mu.Lock()
	h.state.LastError = reason
	h.state.Restarts++
	now := time.Now()
	h.recent = append(h.recent, now)
	cut := now.Add(-h.crashWindow)
	kept := h.recent[:0]
	for _, at := range h.recent {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	h.recent = kept
	limit := h.crashLimit
	previous := h.state.Previous
	current := h.state.Build
	h.failed[current] = true
	canRollBack := previous != "" && previous != current && !h.failed[previous]
	h.mu.Unlock()
	h.save()
	slog.Warn("boti: child failed", "build", current, "reason", reason, "recent", len(kept), "limit", limit)

	if len(kept) < limit || !canRollBack {
		return
	}
	if err := h.switchBuild(previous); err != nil {
		slog.Error("boti: rollback", "build", previous, "err", err)
		return
	}
	h.mu.Lock()
	h.state.Build, h.state.Previous = previous, current
	h.state.LastError = fmt.Sprintf("rolled back to %s after %d failures: %s", previous, len(kept), reason)
	h.recent = nil
	h.mu.Unlock()
	h.save()
	slog.Warn("boti: rolled back", "to", previous, "from", current)
}

func (h *host) sleep(ctx context.Context, wait time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(wait):
		return true
	}
}

func (h *host) loop(ctx context.Context) error {
	defer close(h.done)
	if err := h.control(ctx); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		id, bin, err := h.build()
		if err != nil {
			h.crashed(err, "build")
			if !h.sleep(ctx, h.backoff) {
				return nil
			}
			continue
		}
		proc, err := h.spawn(id, bin)
		if err != nil {
			h.crashed(err, "start")
			if !h.sleep(ctx, h.backoff) {
				return nil
			}
			continue
		}
		if !h.awaitHealthy(ctx, proc) {
			if ctx.Err() != nil {
				h.stop(proc)
				return nil
			}
			reason := "not healthy within " + h.startTimeout.String()
			select {
			case <-proc.done:
				reason = "exited before healthy"
			default:
			}
			h.stop(proc)
			h.crashed(nil, reason)
			if !h.sleep(ctx, h.backoff) {
				return nil
			}
			continue
		}
		select {
		case <-ctx.Done():
			h.stop(proc)
			return nil
		case <-h.restart:
			slog.Info("boti: restart requested")
			h.stop(proc)
		case <-proc.done:
			h.clearChild()
			h.crashed(proc.waitErr, "exited")
			if !h.sleep(ctx, h.backoff) {
				return nil
			}
		}
	}
}

func (h *host) control(ctx context.Context) error {
	_ = os.Remove(h.paths.sock)
	listener, err := net.Listen("unix", h.paths.sock)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(h.paths.sock, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		data, _ := json.Marshal(h.state)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		h.signalRestart()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /activate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Build string `json:"build"`
			When  string `json:"when"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !h.hasBuild(req.Build) {
			http.Error(w, "unknown build", http.StatusNotFound)
			return
		}
		if req.When == "idle" && h.cfg.Idle != "" {
			h.arm(req.Build)
			writeJSON(w, http.StatusOK, activateReply{Build: req.Build, Pending: true})
			return
		}
		if err := h.switchTo(req.Build); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.signalRestart()
		writeJSON(w, http.StatusOK, activateReply{Build: req.Build, Activated: true})
	})
	mux.HandleFunc("PUT /vault-key", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		key, err := hex.DecodeString(req.Key)
		if err != nil || len(key) != 32 {
			http.Error(w, "a vault key is 32 bytes of hex", http.StatusBadRequest)
			return
		}
		h.setVaultKey(key)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /vault-key", func(w http.ResponseWriter, r *http.Request) {
		h.setVaultKey(nil)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /rollback", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		previous := h.state.Previous
		h.mu.Unlock()
		if previous == "" {
			http.Error(w, "no previous build", http.StatusConflict)
			return
		}
		if err := h.switchTo(previous); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.signalRestart()
		writeJSON(w, http.StatusOK, map[string]string{"build": previous})
	})
	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = server.Close()
		_ = os.Remove(h.paths.sock)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("boti: control socket", "err", err)
		}
	}()
	return nil
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func hostEnv(paths hostPaths, bc botConfig) []string {
	env := os.Environ()
	env = setEnv(env, "BOT_ROOT", paths.root)
	env = setEnvIfUnset(env, "BOT_DATA", paths.data)
	for k, v := range bc.Env {
		env = setEnvIfUnset(env, k, v)
	}
	return env
}

func (h *host) copyVaultKey() []byte {
	h.vaultMu.Lock()
	defer h.vaultMu.Unlock()
	return append([]byte(nil), h.vaultKey...)
}

func (h *host) setVaultKey(key []byte) {
	h.vaultMu.Lock()
	h.vaultKey = append([]byte(nil), key...)
	h.vaultMu.Unlock()
}

func withoutEnv(env []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, key := range keys {
		drop[key] = true
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[key] {
			out = append(out, entry)
		}
	}
	return out
}

func controlPost(sock, path string, body []byte) ([]byte, error) {
	return controlDo(sock, http.MethodPost, path, body)
}

func controlDo(sock, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://unix"+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := controlClient(sock).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if message := strings.TrimSpace(string(data)); message != "" {
			return nil, errors.New(message)
		}
		return nil, errors.New(resp.Status)
	}
	return data, nil
}

func controlStatus(sock string) (hostState, error) {
	resp, err := controlClient(sock).Get("http://unix/status")
	if err != nil {
		return hostState{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var state hostState
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&state); err != nil {
		return hostState{}, err
	}
	return state, nil
}

func controlClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
}

func runServe(ctx context.Context, root string) error {
	bc, found, err := readBotConfig(root)
	if err != nil {
		return err
	}
	if !found {
		fmt.Fprintf(os.Stderr, "boti: no bot.toml in %s; serving the default webi\n", root)
	}
	paths := hostPathsFor(root)
	if err := os.MkdirAll(paths.dir, 0o755); err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(paths.dir, "lock"), "another `boti serve` is running for this bot")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	h, err := newHost(root, bc)
	if err != nil {
		return err
	}
	defer h.close()
	if err := runHooks(bc, h.env, h.log, h.log); err != nil {
		return err
	}
	return h.loop(ctx)
}

func runHooks(bc botConfig, env []string, stdout, stderr io.Writer) error {
	for _, hook := range bc.PreRun {
		if hook.Command == "" {
			continue
		}
		cmd := exec.Command("/bin/sh", "-c", hook.Command)
		cmd.Env = env
		cmd.Dir = envValue(env, "BOT_ROOT")
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pre_run %q: %w", hook.Command, err)
		}
	}
	return nil
}

func openHostLog(path string) (*os.File, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > 4<<20 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func status(root string, paths hostPaths, asJSON bool) error {
	data, err := os.ReadFile(paths.state)
	if err != nil {
		return fmt.Errorf("no supervisor state in %s (is `boti serve` running?)", paths.dir)
	}
	if asJSON {
		fmt.Print(string(data))
		return nil
	}
	var state hostState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse %s: %w", paths.state, err)
	}
	fmt.Print(statusText(state, processAlive(state.PID)))
	if !processAlive(state.PID) {
		return fmt.Errorf("not running (last pid %d)", state.PID)
	}
	return nil
}

func statusText(state hostState, alive bool) string {
	var out strings.Builder
	if alive && !state.Started.IsZero() {
		fmt.Fprintf(&out, "boti: running (pid %d, up %s)\n", state.PID, time.Since(state.Started).Round(time.Second))
	} else if alive {
		fmt.Fprintf(&out, "boti: running (pid %d)\n", state.PID)
	} else {
		fmt.Fprintf(&out, "boti: not running (last pid %d)\n", state.PID)
	}
	fmt.Fprintf(&out, "build: %s\n", state.Build)
	if state.Previous != "" {
		fmt.Fprintf(&out, "previous: %s\n", state.Previous)
	}
	if state.Child.PID > 0 {
		health := "starting"
		if state.Child.Healthy {
			health = "healthy"
		}
		up := ""
		if !state.Child.Started.IsZero() {
			up = ", up " + time.Since(state.Child.Started).Round(time.Second).String()
		}
		fmt.Fprintf(&out, "child: pid %d, %s%s\n", state.Child.PID, health, up)
	} else {
		out.WriteString("child: none\n")
	}
	if state.Pending != "" {
		fmt.Fprintf(&out, "pending: %s (waiting for idle)\n", state.Pending)
	}
	fmt.Fprintf(&out, "restarts: %d\n", state.Restarts)
	if state.LastError != "" {
		fmt.Fprintf(&out, "last error: %s\n", state.LastError)
	}
	return out.String()
}

func logs(root string, paths hostPaths, lines int) error {
	text, err := tailFile(paths.log, lines)
	if err != nil {
		return fmt.Errorf("no log in %s (is `boti serve` running?)", paths.dir)
	}
	fmt.Print(text)
	return nil
}

func tailFile(path string, lines int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n") + "\n", nil
}

func restart(root string, paths hostPaths) error {
	resp, err := controlClient(paths.sock).Post("http://unix/restart", "application/json", nil)
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("restart: %s", resp.Status)
	}
	fmt.Println("boti: restart requested")
	return nil
}

func lookPath(name, pathEnv string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, nil
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}

func lockFile(path, busy string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New(busy)
	}
	return file, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".boti-state-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
