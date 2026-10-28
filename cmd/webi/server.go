package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
)

// Settings are the per-install overrides webi passes to ax.
type Settings struct {
	Name      string  `json:"name,omitempty"` // how the UI labels the person typing
	Model     string  `json:"model,omitempty"`
	BaseURL   string  `json:"base_url,omitempty"`
	Effort    string  `json:"effort,omitempty"`
	CompactAt float64 `json:"compact_at,omitempty"`
	Context   int     `json:"context_window,omitempty"`
}

type Server struct {
	store    *Store
	cfg      axrun.Config
	static   http.Handler
	settings string // path to settings.json
	vault    *Vault
	resume   resumeState
	started  time.Time
	build    string
	token    string

	mu       sync.Mutex
	runs     map[string]*axrun.Run
	starting map[string]bool
	subs     map[chan []byte]struct{}
	pending  map[string]pendingSecret
	pins     *pinGuard
}

func NewServer(store *Store, cfg axrun.Config, stateDir string, static http.Handler, build string) *Server {
	return &Server{
		store:    store,
		cfg:      cfg,
		static:   static,
		settings: filepath.Join(stateDir, "settings.json"),
		vault:    NewVault(filepath.Join(stateDir, "vault.json")),
		started:  time.Now(),
		build:    build,
		runs:     map[string]*axrun.Run{},
		starting: map[string]bool{},
		resume:   resumeState{marks: map[string]sessionMark{}},
		subs:     map[chan []byte]struct{}{},
		pins:     newPinGuard(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"ok":     "1",
			"build":  s.build,
			"uptime": int(time.Since(s.started).Seconds()),
		})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": "1", "build": s.build, "running": s.runningCount()})
	})
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("POST /api/settings", s.handlePostSettings)
	mux.HandleFunc("GET /api/dirs", s.handleDirs)
	mux.HandleFunc("GET /api/routines", s.handleRoutines)
	mux.HandleFunc("POST /api/routines/{name}/run", s.handleRunRoutine)
	mux.HandleFunc("GET /api/routines/{name}/log", s.handleRoutineLog)
	mux.HandleFunc("GET /api/routines/{name}/current", s.handleRoutineCurrent)
	mux.HandleFunc("GET /api/routines/{name}/runs", s.handleRoutineRuns)
	mux.HandleFunc("GET /api/routines/{name}/runs/{run}", s.handleRoutineRunLog)
	mux.HandleFunc("GET /api/memory", s.handleMemoryList)
	mux.HandleFunc("GET /api/memory/{id}", s.handleMemoryGet)
	mux.HandleFunc("DELETE /api/memory/{id}", s.handleMemoryDelete)
	mux.HandleFunc("GET /api/skills", s.handleSkills)
	mux.HandleFunc("GET /api/skills/{name}", s.handleSkillGet)
	mux.HandleFunc("GET /api/vault", s.handleVaultState)
	mux.HandleFunc("POST /api/vault/{action}", s.handleVaultAction)
	mux.HandleFunc("POST /api/channels", s.handleCreateChannel)
	mux.HandleFunc("PATCH /api/channels/{id}", s.handleUpdateChannel)
	mux.HandleFunc("DELETE /api/channels/{id}", s.handleDeleteChannel)
	mux.HandleFunc("POST /api/channels/{id}/threads", s.handleCreateThread)
	mux.HandleFunc("GET /api/threads/{id}", s.handleGetThread)
	mux.HandleFunc("PATCH /api/threads/{id}", s.handleRenameThread)
	mux.HandleFunc("DELETE /api/threads/{id}", s.handleDeleteThread)
	mux.HandleFunc("POST /api/threads/{id}/messages", s.handlePostMessage)
	mux.HandleFunc("POST /api/threads/{id}/stop", s.handleStop)
	mux.HandleFunc("POST /api/threads/{id}/compact", s.handleCompact)
	mux.HandleFunc("POST /api/blobs", s.handleUpload)
	mux.HandleFunc("GET /api/blobs/{id}", s.handleBlob)
	mux.HandleFunc("GET /", s.handleStatic)
	return securityHeaders(mux)
}

var errThreadRunning = errors.New("the thread is already running")

// reserveRun closes the gap between "is it running?" and "start it": two
// messages arriving together must not spawn two ax processes on one session.
func (s *Server) reserveRun(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[id] != nil || s.starting[id] {
		return false
	}
	s.starting[id] = true
	return true
}

func (s *Server) releaseRun(id string) {
	s.mu.Lock()
	delete(s.starting, id)
	s.mu.Unlock()
}

// --- SSE -------------------------------------------------------------------

func (s *Server) subscribe() chan []byte {
	ch := make(chan []byte, 64)
	s.mu.Lock()
	if s.subs == nil {
		s.subs = map[chan []byte]struct{}{}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan []byte) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
}

func (s *Server) publish(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- data:
		default:
		}
	}
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	ch := s.subscribe()
	defer s.unsubscribe(ch)
	fmt.Fprintf(w, "data: {\"type\":\"hello\",\"text\":\"webi\"}\n\n")
	flusher.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// --- state -----------------------------------------------------------------

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	settings := s.loadSettings()
	channels := s.store.Channels()
	out := make([]map[string]any, 0, len(channels))
	running := 0
	for _, channel := range channels {
		threads := s.store.Threads(channel.ID)
		for i := range threads {
			threads[i].Running = s.running(threads[i].ID)
			if threads[i].Running {
				running++
			}
		}
		out = append(out, map[string]any{
			"id":          channel.ID,
			"name":        channel.Name,
			"workdir":     channel.Workdir,
			"prompt":      channel.Prompt,
			"pinned":      channel.Pinned,
			"vault_scope": normalizeScope(channel.VaultScope),
			"threads":     threads,
		})
	}
	name := strings.TrimSpace(settings.Name)
	config := map[string]any{
		"name":        name,
		"model":       firstNonEmpty(settings.Model, s.cfg.Model, "deepseek-flash"),
		"effort":      firstNonEmpty(settings.Effort, "max"),
		"has_key":     s.hasKey(),
		"repo":        s.cfg.BotRoot,
		"data_dir":    s.cfg.BotRoot,
		"max_context": firstNonZero(settings.Context, 1_000_000),
		"compact_at":  firstNonZeroFloat(settings.CompactAt, 0.5),
	}
	writeJSON(w, 200, map[string]any{
		"user":           name,
		"build":          s.build,
		"running":        running,
		"uptime":         int(time.Since(s.started).Seconds()),
		"vault":          s.vault.Status(),
		"pending_secret": s.pendingSecrets(),
		"config":         config,
		"channels":       out,
	})
}

func (s *Server) loadSettings() Settings {
	var settings Settings
	if data, err := os.ReadFile(s.settings); err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	return settings
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.loadSettings()
	writeJSON(w, 200, map[string]any{
		"name":           strings.TrimSpace(settings.Name),
		"model":          firstNonEmpty(settings.Model, s.cfg.Model, "deepseek-flash"),
		"base_url":       firstNonEmpty(settings.BaseURL, s.cfg.BaseURL, "https://api.deepseek.com"),
		"effort":         firstNonEmpty(settings.Effort, "max"),
		"compact_at":     firstNonZeroFloat(settings.CompactAt, 0.5),
		"context_window": firstNonZero(settings.Context, 1_000_000),
	})
}

func (s *Server) handlePostSettings(w http.ResponseWriter, r *http.Request) {
	var req Settings
	if !readJSON(w, r, &req) {
		return
	}
	if req.CompactAt < 0 || req.CompactAt >= 1 {
		writeErr(w, 400, "compact_at is a fraction of the window between 0 and 1")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len([]rune(req.Name)) > 40 {
		req.Name = string([]rune(req.Name)[:40])
	}
	data, _ := json.MarshalIndent(req, "", "  ")
	if err := writeFileAtomic(s.settings, data, 0o600); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.publish(map[string]any{"type": "state"})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDirs(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(r.URL.Query().Get("path"))
	if root == "" {
		home, _ := os.UserHomeDir()
		root = home
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	dirs := []map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dirs = append(dirs, map[string]string{"name": entry.Name(), "path": filepath.Join(root, entry.Name())})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"] < dirs[j]["name"] })
	parent := filepath.Dir(root)
	writeJSON(w, 200, map[string]any{"path": root, "parent": parent, "dirs": dirs})
}

// --- channels --------------------------------------------------------------

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Workdir string `json:"workdir"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	workdir := strings.TrimSpace(req.Workdir)
	if workdir == "" {
		writeErr(w, 400, "workdir is required")
		return
	}
	if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		writeErr(w, 400, "workdir is not a directory: "+workdir)
		return
	}
	id, err := randomID()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	channel := Channel{ID: id, Name: firstNonEmpty(strings.TrimSpace(req.Name), filepath.Base(workdir)), Workdir: workdir, Created: time.Now().UTC()}
	if err := s.store.PutChannel(channel); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.publish(map[string]any{"type": "state"})
	writeJSON(w, 200, channel)
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	channel, ok := s.store.Channel(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "unknown channel")
		return
	}
	var req struct {
		Name   *string `json:"name"`
		Prompt *string `json:"prompt"`
		Pinned *bool   `json:"pinned"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		channel.Name = strings.TrimSpace(*req.Name)
	}
	if req.Prompt != nil {
		channel.Prompt = *req.Prompt
	}
	if req.Pinned != nil {
		channel.Pinned = *req.Pinned
	}
	if err := s.store.PutChannel(channel); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.publish(map[string]any{"type": "state"})
	writeJSON(w, 200, channel)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, thread := range s.store.Threads(id) {
		_ = s.store.DeleteThread(thread.ID)
	}
	if err := s.store.DeleteChannel(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.publish(map[string]any{"type": "state"})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	_ = json.NewDecoder(r.Body).Decode(&req)
	thread, err := s.store.CreateThread(r.PathValue("id"), req.Title)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.publish(map[string]any{"type": "state"})
	writeJSON(w, 200, thread)
}

// --- threads ---------------------------------------------------------------

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	thread, err := s.store.Thread(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	events, err := readSession(s.store.SessionPath(thread.ID))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"thread":   thread,
		"messages": events,
		"running":  s.running(thread.ID),
	})
}

func (s *Server) handleRenameThread(w http.ResponseWriter, r *http.Request) {
	thread, err := s.store.Thread(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	thread.Title = strings.TrimSpace(req.Title)
	_ = s.store.PutThread(thread)
	s.publish(map[string]any{"type": "state"})
	writeJSON(w, 200, thread)
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.stop(id)
	if err := s.store.DeleteThread(id); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	s.publish(map[string]any{"type": "state"})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	thread, err := s.store.Thread(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	var req struct {
		Text   string   `json:"text"`
		Images []string `json:"images"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Images) == 0 {
		writeErr(w, 400, "empty message")
		return
	}
	if s.running(thread.ID) {
		// While ax is running, a message steers the run instead of starting a
		// new one: it lands before the next model call of the same turn loop.
		if len(req.Images) > 0 {
			writeErr(w, 409, "images cannot be attached to a steering message; wait for the run to finish")
			return
		}
		if err := s.steer(thread.ID, req.Text); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		s.store.Touch(thread.ID, "")
		s.publish(map[string]any{"type": "message", "thread": thread.ID, "message": map[string]any{
			"t": "user", "text": req.Text, "ts": time.Now().UTC(), "steer": true,
		}})
		writeJSON(w, 200, map[string]bool{"ok": true, "steered": true})
		return
	}
	images, err := s.prepareImages(thread, req.Images)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Start first: if another request won the race there is no run, and the
	// message should not appear in the thread.
	if err := s.startRun(thread, req.Text, images); err != nil {
		if errors.Is(err, errThreadRunning) {
			writeErr(w, 409, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.store.Touch(thread.ID, req.Text)
	s.publish(map[string]any{"type": "message", "thread": thread.ID, "message": map[string]any{
		"t": "user", "text": req.Text, "ts": time.Now().UTC(),
	}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) prepareImages(thread Thread, ids []string) ([]string, error) {
	var paths []string
	for _, id := range ids {
		source, ok := s.store.BlobPath(id)
		if !ok {
			return nil, fmt.Errorf("unknown image %s", id)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return nil, err
		}
		ext := filepath.Ext(source)
		target := filepath.Join(s.store.Blobs(thread), strings.TrimSuffix(filepath.Base(source), ext)+".img"+ext)
		if err := writeFileAtomic(target, data, 0o600); err != nil {
			return nil, err
		}
		paths = append(paths, target)
	}
	return paths, nil
}

// startRun launches ax for one user turn and streams its events to the UI.
func (s *Server) startRun(thread Thread, prompt string, images []string) error {
	return s.launch(thread, axrun.Job{
		Session: s.store.SessionPath(thread.ID),
		Prompt:  prompt,
		Images:  images,
	}, prompt)
}

// startResume continues a run whose ax process died, with the prompt ax writes
// itself from the session log. Title generation is skipped: the thread already
// has a name by the time it can be interrupted.
func (s *Server) startResume(thread Thread) error {
	return s.launch(thread, axrun.Job{
		Session: s.store.SessionPath(thread.ID),
		Resume:  true,
	}, "")
}

func (s *Server) launch(thread Thread, job axrun.Job, prompt string) error {
	if !s.reserveRun(thread.ID) {
		return errThreadRunning
	}
	channel, ok := s.store.Channel(thread.Channel)
	if !ok {
		s.releaseRun(thread.ID)
		return fmt.Errorf("unknown channel %s", thread.Channel)
	}
	settings := s.loadSettings()
	cfg := s.cfg
	cfg.BotRoot = channel.Workdir
	cfg.Model = firstNonEmpty(settings.Model, cfg.Model)
	cfg.BaseURL = firstNonEmpty(settings.BaseURL, cfg.BaseURL)
	// A channel is a bot: its runtime data lives at its root, not at the
	// host's BOT_DATA. Without this every channel would share one workspace.
	env := []string{
		"AX_WEB_THREAD=" + thread.ID,
		"AX_WEB_CHANNEL=" + channel.ID,
		"BOT_DATA=" + channel.Workdir,
	}
	if channel.Prompt != "" {
		env = append(env, "AX_WEB_PROMPT="+channel.Prompt)
	}
	vaultEnv, cleanupVault, err := s.vault.Env(channel.VaultScope)
	if err != nil {
		s.releaseRun(thread.ID)
		return err
	}
	env = append(env, vaultEnv...)
	cfg.Env = append(cfg.Env, env...)

	run, err := axrun.Start(context.Background(), cfg, job, func(event axrun.Event) {
		s.forwardEvent(thread.ID, event)
	})
	if err != nil {
		s.releaseRun(thread.ID)
		return err
	}
	s.mu.Lock()
	s.runs[thread.ID] = run
	delete(s.starting, thread.ID)
	s.mu.Unlock()
	s.publish(map[string]any{"type": "run", "thread": thread.ID, "running": true})

	go func() {
		result, err := run.Wait()
		cleanupVault()
		s.mu.Lock()
		if s.runs[thread.ID] == run {
			delete(s.runs, thread.ID)
		}
		s.mu.Unlock()
		if err != nil {
			slog.Warn("webi: ax run", "thread", thread.ID, "error", err)
			s.publish(map[string]any{"type": "message", "thread": thread.ID, "message": map[string]any{
				"t": "error", "text": err.Error(), "ts": time.Now().UTC(),
			}})
		}
		s.store.Touch(thread.ID, "")
		if err == nil && !job.Resume && result.Text != "" && !thread.TitleGenerated {
			go s.generateTitle(thread, prompt, result.Text)
		}
		s.publish(map[string]any{"type": "run", "thread": thread.ID, "running": false})
		s.publish(map[string]any{"type": "state"})
	}()
	return nil
}

func (s *Server) forwardEvent(threadID string, event axrun.Event) {
	switch event.Type {
	case "assistant_delta":
		s.publish(map[string]any{"type": "delta", "thread": threadID, "id": "live", "text": event.Text})
	case "message":
		msg, ok := event.MessageBody()
		if !ok || strings.TrimSpace(msg.Content) == "" {
			return
		}
		if msg.Role != "assistant" {
			return
		}
		s.publish(map[string]any{"type": "message", "thread": threadID, "message": map[string]any{
			"t": "assistant", "text": msg.Content, "ts": time.Now().UTC(),
		}})
	case "tool_start":
		target := event.Target
		if target == "" {
			target = event.Name
		}
		s.publish(map[string]any{"type": "step", "thread": threadID, "tool": target})
	case "compaction":
		s.publish(map[string]any{"type": "compact", "thread": threadID, "upto": event.Upto})
	case "secret_request":
		s.addPendingSecret(threadID, event)
	}
}

func (s *Server) runningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func (s *Server) running(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id] != nil
}

func (s *Server) steer(id, text string) error {
	s.mu.Lock()
	run := s.runs[id]
	s.mu.Unlock()
	if run == nil {
		return fmt.Errorf("the run finished; send the message again")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty message")
	}
	return run.Steer(text)
}

func (s *Server) stop(id string) bool {
	s.mu.Lock()
	run := s.runs[id]
	s.mu.Unlock()
	if run == nil {
		return false
	}
	_ = run.Cancel()
	return true
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"stopped": s.stop(r.PathValue("id"))})
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	thread, err := s.store.Thread(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	if s.running(thread.ID) {
		writeErr(w, 409, "the thread is running")
		return
	}
	channel, ok := s.store.Channel(thread.Channel)
	if !ok {
		writeErr(w, 404, "unknown channel")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	_, err = axrun.Compact(ctx, axrun.Config{
		AXPath:  s.cfg.AXPath,
		BotRoot: channel.Workdir,
		Model:   firstNonEmpty(s.loadSettings().Model, s.cfg.Model),
	}, s.store.SessionPath(thread.ID), func(event axrun.Event) {
		s.publish(map[string]any{"type": "compact", "thread": thread.ID, "upto": event.Upto})
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.publish(map[string]any{"type": "compact", "thread": thread.ID})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// --- blobs -----------------------------------------------------------------

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id, err := s.store.SaveBlob(data, "")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"id": id, "url": "/api/blobs/" + id})
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	path, ok := s.store.BlobPath(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "unknown blob")
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.static == nil {
		writeErr(w, 404, "no ui")
		return
	}
	s.static.ServeHTTP(w, r)
}

// --- helpers ---------------------------------------------------------------

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(target); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeErr(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroFloat(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
