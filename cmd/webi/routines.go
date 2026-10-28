package main

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/3-lines-studio/ax/internal/botdir"
	"github.com/3-lines-studio/ax/internal/routine"
)

const maxRoutineLog = 64 << 10

type routineView struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Schedule    string             `json:"schedule,omitempty"`
	Timezone    string             `json:"timezone,omitempty"`
	Enabled     bool               `json:"enabled"`
	Last        *routine.RunResult `json:"last,omitempty"`
}

func (s *Server) routinePaths() botdir.Paths {
	return botdir.Resolve(s.cfg.BotRoot)
}

func (s *Server) handleRoutines(w http.ResponseWriter, _ *http.Request) {
	paths := s.routinePaths()
	set, err := routine.Open(paths)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	runner := routine.NewRunner(paths)
	views := make([]routineView, 0, len(set.Items))
	for _, item := range set.Items {
		view := routineView{
			Name:        item.Name,
			Description: item.Description,
			Enabled:     item.Enabled,
			Timezone:    item.Location.String(),
		}
		if item.Schedule != nil {
			view.Schedule = item.Schedule.String()
		}
		if last, err := runner.LastRun(item.Name); err == nil {
			view.Last = &last
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"routines": views})
}

func (s *Server) handleRunRoutine(w http.ResponseWriter, req *http.Request) {
	paths := s.routinePaths()
	set, err := routine.Open(paths)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	item, err := set.Find(req.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "routine not found")
		return
	}
	result, err := routine.NewRunner(paths).Run(context.Background(), item, "manual")
	if errors.Is(err, routine.ErrRunning) {
		writeErr(w, http.StatusConflict, "routine is already running")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": result, "log": tailFile(result.Log)})
}

func (s *Server) handleRoutineLog(w http.ResponseWriter, req *http.Request) {
	paths := s.routinePaths()
	runner := routine.NewRunner(paths)
	last, err := runner.LastRun(req.PathValue("name"))
	if err != nil {
		if errors.Is(err, routine.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no runs yet")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": last, "log": tailFile(last.Log)})
}

func tailFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxRoutineLog {
		data = data[len(data)-maxRoutineLog:]
	}
	return string(data)
}
