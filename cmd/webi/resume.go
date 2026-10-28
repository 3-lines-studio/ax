package main

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// A run whose ax process died leaves a `run_start` with no `run_end` in the
// thread's session. ax can continue it (`--resume`); webi decides when: once at
// startup and then on a timer, so a crash or a deploy does not strand work.

// resumeInterval is how often the sweep looks for interrupted runs.
const resumeInterval = 30 * time.Second

type sessionMark struct {
	size    int64
	modTime time.Time
	open    bool
}

// resumeState caches the open/closed verdict per session file: scanning every
// log on every tick would re-read megabytes of history for nothing.
type resumeState struct {
	mu    sync.Mutex
	marks map[string]sessionMark
}

// StartResumeLoop sweeps now and then every interval until ctx is done.
func (s *Server) StartResumeLoop(interval time.Duration) {
	s.resumeOpenRuns()
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			s.resumeOpenRuns()
		}
	}()
}

// resumeOpenRuns starts one ax --resume per interrupted thread that is not
// already running here.
func (s *Server) resumeOpenRuns() {
	for _, channel := range s.store.Channels() {
		for _, thread := range s.store.Threads(channel.ID) {
			if s.running(thread.ID) {
				continue
			}
			path := s.store.SessionPath(thread.ID)
			open, err := s.sessionOpen(path)
			if err != nil {
				slog.Warn("webi: resume scan", "thread", thread.ID, "error", err)
				continue
			}
			if !open {
				continue
			}
			slog.Info("webi: resuming interrupted run", "thread", thread.ID)
			if err := s.startResume(thread); err != nil {
				slog.Warn("webi: resume", "thread", thread.ID, "error", err)
			}
		}
	}
}

// sessionOpen reports whether the log ends inside a run. The answer is cached
// by size and mtime, so an untouched log costs one stat per sweep.
func (s *Server) sessionOpen(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	s.resume.mu.Lock()
	if mark, ok := s.resume.marks[path]; ok {
		if mark.size == info.Size() && mark.modTime.Equal(info.ModTime()) {
			s.resume.mu.Unlock()
			return mark.open, nil
		}
	}
	s.resume.mu.Unlock()

	open, err := scanOpenRun(path)
	if err != nil {
		return false, err
	}
	s.resume.mu.Lock()
	s.resume.marks[path] = sessionMark{size: info.Size(), modTime: info.ModTime(), open: open}
	s.resume.mu.Unlock()
	return open, nil
}

// scanOpenRun walks the log and answers whether the last run lifecycle entry is
// a start. Broken lines are skipped: a torn tail must not stop the sweep.
func scanOpenRun(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = file.Close() }()
	open := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		switch entry.Type {
		case "run_start":
			open = true
		case "run_end":
			open = false
		}
	}
	return open, scanner.Err()
}
