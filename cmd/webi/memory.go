package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/3-lines-studio/ax/internal/memory"
)

func (s *Server) memoryTarget(channelID string) (*memory.Store, botTarget, error) {
	target, err := s.botTarget(channelID)
	if err != nil {
		return nil, botTarget{}, err
	}
	return memory.New(filepath.Join(target.Paths.State, "memory")), target, nil
}

func (s *Server) handleMemoryList(w http.ResponseWriter, req *http.Request) {
	store, target, err := s.memoryTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	limit := 50
	if value := req.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			limit = parsed
		}
	}
	var items []memory.Memory
	if query := req.URL.Query().Get("q"); query != "" {
		items, err = store.Search(query, limit)
	} else {
		items, err = store.List(limit)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []memory.Memory{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel":      target.Channel,
		"channel_name": target.ChannelName,
		"memories":     items,
	})
}

func (s *Server) handleMemoryGet(w http.ResponseWriter, req *http.Request) {
	store, _, err := s.memoryTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	item, err := store.Get(req.PathValue("id"))
	if errors.Is(err, memory.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "memory not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memory": item})
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, req *http.Request) {
	store, _, err := s.memoryTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	force := req.URL.Query().Get("force") == "1" || req.URL.Query().Get("force") == "true"
	err = store.Delete(req.PathValue("id"), force)
	if errors.Is(err, memory.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "memory not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "force": force})
}
