package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/axrun"
)

func TestSkillsAPI(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkillFile(t, filepath.Join(root, "skills"), "deploy", "---\ndescription: Ship a release\n---\nRun the pipeline.")
	if err := os.MkdirAll(filepath.Join(root, "skills", "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	channels := store.Channels()
	channels[0].Workdir = root
	if err := store.PutChannel(channels[0]); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, cfg: axrun.Config{BotRoot: root}}

	rec := httptest.NewRecorder()
	srv.handleSkills(rec, httptest.NewRequest("GET", "/api/skills?channel="+channels[0].ID, nil))
	if rec.Code != 200 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Skills []skillView `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Skills) != 2 {
		t.Fatalf("skills = %+v", list.Skills)
	}
	byName := map[string]skillView{}
	for _, item := range list.Skills {
		byName[item.Name] = item
	}
	if byName["deploy"].Description != "Ship a release" || byName["deploy"].Source != "bot" || byName["deploy"].Error != "" {
		t.Fatalf("deploy = %+v", byName["deploy"])
	}
	if !strings.Contains(byName["broken"].Error, "missing") {
		t.Fatalf("broken = %+v", byName["broken"])
	}

	req := httptest.NewRequest("GET", "/api/skills/deploy?channel="+channels[0].ID, nil)
	req.SetPathValue("name", "deploy")
	rec = httptest.NewRecorder()
	srv.handleSkillGet(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Run the pipeline.") {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest("GET", "/api/skills/../escape", nil)
	req.SetPathValue("name", "../escape")
	rec = httptest.NewRecorder()
	srv.handleSkillGet(rec, req)
	if rec.Code != 404 {
		t.Fatalf("escape = %d %s", rec.Code, rec.Body)
	}
}

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsFallsBackToUserRoots(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkillFile(t, filepath.Join(home, ".agents", "skills"), "personal", "My home skill")
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	channels := store.Channels()
	channels[0].Workdir = root
	if err := store.PutChannel(channels[0]); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, cfg: axrun.Config{BotRoot: root}}
	rec := httptest.NewRecorder()
	srv.handleSkills(rec, httptest.NewRequest("GET", "/api/skills?channel="+channels[0].ID, nil))
	var list struct {
		Skills []skillView `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Skills) != 1 || list.Skills[0].Name != "personal" || list.Skills[0].Source != "user" {
		t.Fatalf("skills = %+v", list.Skills)
	}
}
