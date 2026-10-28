package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/3-lines-studio/ax/internal/botdir"
	"github.com/3-lines-studio/ax/internal/routine"
)

const maxRoutineLog = 64 << 10

type routineView struct {
	Channel     string             `json:"channel,omitempty"`
	ChannelName string             `json:"channel_name,omitempty"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Schedule    string             `json:"schedule,omitempty"`
	Timezone    string             `json:"timezone,omitempty"`
	Enabled     bool               `json:"enabled"`
	Last        *routine.RunResult `json:"last,omitempty"`
}

type routineTarget struct {
	Paths       botdir.Paths
	Channel     string
	ChannelName string
}

func (s *Server) routineTarget(channelID string) (routineTarget, error) {
	if channelID == "" || s.store == nil {
		return routineTarget{Paths: botdir.Resolve(s.cfg.BotRoot)}, nil
	}
	channel, ok := s.store.Channel(channelID)
	if !ok {
		return routineTarget{}, errors.New("channel not found")
	}
	return routineTarget{
		Paths:       botdir.Resolve(channel.Workdir),
		Channel:     channel.ID,
		ChannelName: channel.Name,
	}, nil
}

func (s *Server) routineTargets(w http.ResponseWriter, req *http.Request) ([]routineTarget, bool) {
	if id := req.URL.Query().Get("channel"); id != "" {
		target, err := s.routineTarget(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return nil, false
		}
		return []routineTarget{target}, true
	}
	if s.store == nil {
		target, _ := s.routineTarget("")
		return []routineTarget{target}, true
	}
	var targets []routineTarget
	for _, channel := range s.store.Channels() {
		targets = append(targets, routineTarget{
			Paths:       botdir.Resolve(channel.Workdir),
			Channel:     channel.ID,
			ChannelName: channel.Name,
		})
	}
	return targets, true
}

func (s *Server) handleRoutines(w http.ResponseWriter, req *http.Request) {
	targets, ok := s.routineTargets(w, req)
	if !ok {
		return
	}
	views := make([]routineView, 0)
	for _, target := range targets {
		set, err := routine.Open(target.Paths)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, target.ChannelName+": "+err.Error())
			return
		}
		runner := routine.NewRunner(target.Paths)
		for _, item := range set.Items {
			view := routineView{
				Channel:     target.Channel,
				ChannelName: target.ChannelName,
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
	}
	writeJSON(w, http.StatusOK, map[string]any{"routines": views})
}

func (s *Server) handleRunRoutine(w http.ResponseWriter, req *http.Request) {
	target, err := s.routineTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	set, err := routine.Open(target.Paths)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	item, err := set.Find(req.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "routine not found")
		return
	}
	result, err := routine.NewRunner(target.Paths).Run(context.Background(), item, "manual")
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
	target, err := s.routineTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	last, err := routine.NewRunner(target.Paths).LastRun(req.PathValue("name"))
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

func (s *Server) handleRoutineRuns(w http.ResponseWriter, req *http.Request) {
	target, err := s.routineTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	limit := 20
	if value := req.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			limit = parsed
		}
	}
	runs, err := routine.NewRunner(target.Paths).Runs(req.PathValue("name"), limit)
	if err != nil {
		if errors.Is(err, routine.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"runs": []routine.RunResult{}})
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleRoutineRunLog(w http.ResponseWriter, req *http.Request) {
	target, err := s.routineTarget(req.URL.Query().Get("channel"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	result, err := routine.NewRunner(target.Paths).RunInfo(req.PathValue("name"), req.PathValue("run"))
	if err != nil {
		if errors.Is(err, routine.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "run not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": result, "log": tailFile(result.Log)})
}

// tailFile reads at most maxRoutineLog bytes from the end of a run log, so a
// chatty routine cannot blow up the API response.
func tailFile(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := int64(0)
	if info.Size() > maxRoutineLog {
		start = info.Size() - maxRoutineLog
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return string(data)
}
