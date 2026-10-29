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

func isSessionBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already in use")
}

const resumeInterval = 30 * time.Second

type sessionMark struct {
	size    int64
	modTime time.Time
	open    bool
}

type resumeState struct {
	mu    sync.Mutex
	marks map[string]sessionMark
}

func (s *Server) StartResumeLoop(interval time.Duration) {
	s.resumeOpenRuns()
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			s.resumeOpenRuns()
		}
	}()
}

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
			if err := s.startResume(thread); err != nil {
				if isSessionBusy(err) {
					slog.Debug("webi: run is alive, not resuming", "thread", thread.ID)
					continue
				}
				slog.Warn("webi: resume", "thread", thread.ID, "error", err)
			} else {
				slog.Info("webi: resuming interrupted run", "thread", thread.ID)
			}
		}
	}
}

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
