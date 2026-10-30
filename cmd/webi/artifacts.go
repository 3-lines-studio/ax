package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const artifactScheme = "artifact:"

// artifactSpec is what attachx reports for a file it copied into
// AX_ARTIFACT_DIR: id, name, media type and size.
type artifactSpec struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

// ArtifactsDir is where attachx drops files for a thread. It is passed to the
// run as AX_ARTIFACT_DIR so the same provider that works for slaxi works here.
func (s *Store) ArtifactsDir(thread string) string {
	return filepath.Join(s.threadDir(thread), "artifacts")
}

// ArtifactPath finds the file behind an artifact id. attachx names files
// <id>-<basename>, so the id is matched as a prefix of a regular file inside
// the thread's artifact directory. Nothing outside that directory resolves.
func (s *Store) ArtifactPath(thread, id string) (string, bool) {
	if !validArtifactID(id) {
		return "", false
	}
	dir := s.ArtifactsDir(thread)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), id+"-") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		return path, true
	}
	return "", false
}

func validArtifactID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// parseArtifactOutput recognizes the JSON payload attachx returns as a tool
// result. Anything else yields ok=false.
func parseArtifactOutput(output string) (artifactSpec, bool) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return artifactSpec{}, false
	}
	var payload struct {
		Artifact *artifactSpec `json:"artifact"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil || payload.Artifact == nil {
		return artifactSpec{}, false
	}
	spec := *payload.Artifact
	if !validArtifactID(spec.ID) || spec.Name == "" {
		return artifactSpec{}, false
	}
	return spec, true
}
