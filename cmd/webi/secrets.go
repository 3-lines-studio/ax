package main

import (
	"fmt"
	"strings"

	"github.com/3-lines-studio/ax/internal/axrun"
)

type pendingSecret struct {
	ID          string `json:"id"`
	Thread      string `json:"thread"`
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Description string `json:"description,omitempty"`
}

func secretID(thread, name string) string {
	return thread + ":" + name
}

func (s *Server) addPendingSecret(threadID string, event axrun.Event) {
	name := strings.TrimSpace(event.Name)
	if name == "" {
		return
	}
	thread, err := s.store.Thread(threadID)
	if err != nil {
		return
	}
	channel, ok := s.store.Channel(thread.Channel)
	if !ok {
		return
	}
	request := pendingSecret{
		ID:          secretID(threadID, name),
		Thread:      threadID,
		Name:        name,
		Scope:       normalizeScope(channel.VaultScope),
		Description: event.Description,
	}
	s.mu.Lock()
	if s.pending == nil {
		s.pending = map[string]pendingSecret{}
	}
	s.pending[request.ID] = request
	s.mu.Unlock()
	s.publish(map[string]any{"type": "state"})
}

func (s *Server) pendingSecrets() []pendingSecret {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pendingSecret, 0, len(s.pending))
	for _, request := range s.pending {
		out = append(out, request)
	}
	return out
}

func (s *Server) takePendingSecret(id string) (pendingSecret, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	return request, ok
}

func (s *Server) answerSecret(request pendingSecret, value string) error {
	if err := s.vault.Put(request.Scope, request.Name, value, ""); err != nil {
		return err
	}
	s.mu.Lock()
	run := s.runs[request.Thread]
	s.mu.Unlock()
	if run != nil {
		if err := run.Secret(request.Name, value); err != nil && !strings.Contains(err.Error(), "finished") {
			return fmt.Errorf("save it in the vault but the run did not take it: %w", err)
		}
	}
	s.publish(map[string]any{"type": "state"})
	return nil
}

func (s *Server) skipSecret(request pendingSecret) error {
	s.mu.Lock()
	run := s.runs[request.Thread]
	s.mu.Unlock()
	if run != nil {
		if err := run.SkipSecret(request.Name); err != nil && !strings.Contains(err.Error(), "finished") {
			return err
		}
	}
	s.publish(map[string]any{"type": "state"})
	return nil
}
