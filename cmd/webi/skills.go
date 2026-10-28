package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/3-lines-studio/ax/internal/skill"
)

type skillView struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Error       string `json:"error,omitempty"`
}

type skillRoot struct {
	Path   string
	Source string
}

// skillRoots mirrors what skillx gives the agent: the bot's skills/ when it
// exists, otherwise the workspace project directories and the user's.
func skillRoots(target botTarget) []skillRoot {
	home, _ := os.UserHomeDir()
	var roots []skillRoot
	for _, path := range skill.Roots(target.Paths.Root, target.Paths.Workspace, home) {
		source := "workspace"
		switch path {
		case filepath.Join(target.Paths.Root, "skills"):
			source = "bot"
		case filepath.Join(home, ".agents", "skills"):
			source = "user"
		}
		roots = append(roots, skillRoot{Path: path, Source: source})
	}
	return roots
}

func (s *Server) handleSkills(w http.ResponseWriter, req *http.Request) {
	target, err := s.botTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	views := make([]skillView, 0)
	seen := make(map[string]struct{})
	for _, root := range skillRoots(target) {
		opened, err := os.OpenRoot(root.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			writeErr(w, http.StatusInternalServerError, root.Path+": "+err.Error())
			return
		}
		items, err := skill.List(opened)
		_ = opened.Close()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, root.Path+": "+err.Error())
			return
		}
		for _, item := range items {
			if _, exists := seen[item.Name]; exists {
				continue
			}
			seen[item.Name] = struct{}{}
			views = append(views, skillView{
				Name:        item.Name,
				Description: item.Description,
				Source:      root.Source,
				Error:       item.Error,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel":      target.Channel,
		"channel_name": target.ChannelName,
		"skills":       views,
	})
}

func (s *Server) handleSkillGet(w http.ResponseWriter, req *http.Request) {
	target, err := s.botTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	name := req.PathValue("name")
	for _, root := range skillRoots(target) {
		opened, err := os.OpenRoot(root.Path)
		if err != nil {
			continue
		}
		item, err := skill.Read(opened, name)
		_ = opened.Close()
		if err != nil {
			continue
		}
		writeJSON(w, http.StatusOK, map[string]any{"skill": map[string]any{
			"name":        item.Name,
			"description": item.Description,
			"body":        item.Body,
			"source":      root.Source,
			"error":       item.Error,
		}})
		return
	}
	writeErr(w, http.StatusNotFound, "skill not found")
}
