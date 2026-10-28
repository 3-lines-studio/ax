// Package routine runs deterministic bot routines: executable code under
// routines/<name>/ that runs on demand or on a cron schedule, with no model in
// the loop.
package routine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
	"github.com/BurntSushi/toml"
)

var (
	// ErrNotFound means the routine does not exist.
	ErrNotFound = errors.New("routine not found")
	// ErrRunning means another run of the same routine holds the lock.
	ErrRunning = errors.New("routine is already running")
)

// Manifest is routines/<name>/routine.toml.
type Manifest struct {
	Description string `toml:"description"`
	Entry       string `toml:"entry"`
	Schedule    string `toml:"schedule"`
	Timezone    string `toml:"timezone"`
	Timeout     string `toml:"timeout"`
	KeepRuns    *int   `toml:"keep_runs"`
	Enabled     bool   `toml:"enabled"`
}

// Routine is a validated routine definition.
type Routine struct {
	Name        string
	Dir         string
	Description string
	Entry       string
	Schedule    *Schedule
	Location    *time.Location
	Timeout     time.Duration
	// KeepRuns is how many run directories survive; 0 keeps every run.
	KeepRuns int
	Enabled  bool
}

// Set is the routines discovered under one bot root.
type Set struct {
	Paths botdir.Paths
	Items []Routine
}

// Open discovers routines in paths.Routines (or <root>/routines). A missing
// directory is an empty set. A malformed manifest fails the whole load so a
// broken routine is visible instead of silently skipped.
func Open(paths botdir.Paths) (*Set, error) {
	dir := paths.Routines
	if dir == "" {
		dir = filepath.Join(paths.Root, "routines")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &Set{Paths: paths}, nil
		}
		return nil, fmt.Errorf("read routines: %w", err)
	}
	set := &Set{Paths: paths}
	for _, entry := range entries {
		if !entry.IsDir() || !validName(entry.Name()) {
			continue
		}
		item, err := load(filepath.Join(dir, entry.Name()), entry.Name())
		if err != nil {
			return nil, err
		}
		if item.Name == "" {
			continue
		}
		set.Items = append(set.Items, item)
	}
	sort.Slice(set.Items, func(i, j int) bool { return set.Items[i].Name < set.Items[j].Name })
	return set, nil
}

// Find returns one routine by name.
func (s *Set) Find(name string) (Routine, error) {
	for _, item := range s.Items {
		if item.Name == name {
			return item, nil
		}
	}
	return Routine{}, ErrNotFound
}

// Runner executes routines for one bot.
type Runner struct {
	Paths botdir.Paths
}

// NewRunner returns a runner for paths.
func NewRunner(paths botdir.Paths) *Runner {
	return &Runner{Paths: paths}
}

// RunResult is the metadata of one run.
type RunResult struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Trigger   string    `json:"trigger"`
	Status    string    `json:"status"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Log       string    `json:"log"`
}

// Run executes one routine. A non-zero exit is reported in the result, not as
// a Go error; ErrRunning means the lock is held by another run.
func (r *Runner) Run(ctx context.Context, item Routine, trigger string) (RunResult, error) {
	entry, err := entryPath(item)
	if err != nil {
		return RunResult{}, err
	}
	state := r.stateDir(item.Name)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return RunResult{}, err
	}
	lock, err := lockRun(filepath.Join(state, "lock"))
	if err != nil {
		return RunResult{}, err
	}
	defer unlockRun(lock)

	r.reconcile(state)

	id, err := runID()
	if err != nil {
		return RunResult{}, err
	}
	runDir := filepath.Join(state, "runs", id)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return RunResult{}, err
	}
	logFile, err := os.OpenFile(filepath.Join(runDir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return RunResult{}, err
	}
	defer logFile.Close()

	result := RunResult{
		ID:        id,
		Name:      item.Name,
		Trigger:   trigger,
		Status:    "running",
		ExitCode:  -1,
		StartedAt: time.Now().UTC(),
		Log:       filepath.Join(runDir, "output.log"),
	}
	_ = r.writeResult(runDir, result)

	runCtx, cancel := context.WithTimeout(ctx, item.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, entry)
	configureCommand(cmd)
	cmd.Dir = r.workspace()
	cmd.Env = envWith(os.Environ(),
		"BOT_ROOT", r.Paths.Root,
		"BOT_DATA", r.dataRoot(),
		"ROUTINE_NAME", item.Name,
		"ROUTINE_DIR", item.Dir,
		"ROUTINE_STATE", state,
		"ROUTINE_RUN_ID", id,
		"ROUTINE_TRIGGER", trigger,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	result.Status = "failed"
	if err := cmd.Start(); err != nil {
		result.Error = err.Error()
		result.EndedAt = time.Now().UTC()
		_ = r.writeResult(runDir, result)
		return result, nil
	}
	waitErr := cmd.Wait()
	result.EndedAt = time.Now().UTC()
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		result.Status = "ok"
		result.ExitCode = 0
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		result.Status = "timeout"
		result.Error = "timeout after " + item.Timeout.String()
	case errors.As(waitErr, &exitErr):
		result.ExitCode = exitErr.ExitCode()
		result.Error = waitErr.Error()
	default:
		result.Error = waitErr.Error()
	}
	_ = r.writeResult(runDir, result)
	r.prune(state, item.KeepRuns)
	return result, nil
}

const (
	defaultKeepRuns = 50
	maxKeepRuns     = 1000
)

// prune deletes the oldest run directories beyond keep. It runs under the
// routine lock, so it cannot race with another run.
func (r *Runner) prune(state string, keep int) {
	if keep <= 0 {
		return
	}
	dir := filepath.Join(state, "runs")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	type runEntry struct {
		dir     string
		id      string
		started time.Time
	}
	runs := make([]runEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validRunID(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		result, err := readResult(path)
		if err != nil {
			// A directory without a record is a crash leftover: prune it first.
			result = RunResult{ID: entry.Name()}
		}
		runs = append(runs, runEntry{dir: path, id: result.ID, started: result.StartedAt})
	}
	if len(runs) <= keep {
		return
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].started.Equal(runs[j].started) {
			return runs[i].id > runs[j].id
		}
		return runs[i].started.After(runs[j].started)
	})
	for _, run := range runs[keep:] {
		_ = os.RemoveAll(run.dir)
	}
}

// reconcile marks runs left in the running state by a host that died as
// interrupted. It runs while the lock is held, so any other "running" record
// in this routine belongs to a process that is gone.
func (r *Runner) reconcile(state string) {
	dir := filepath.Join(state, "runs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if !entry.IsDir() || !validRunID(entry.Name()) {
			continue
		}
		runDir := filepath.Join(dir, entry.Name())
		result, err := readResult(runDir)
		if err != nil || result.Status != "running" {
			continue
		}
		result.Status = "interrupted"
		result.Error = "interrupted: the host stopped before the run finished"
		if result.EndedAt.IsZero() {
			result.EndedAt = now
		}
		_ = r.writeResult(runDir, result)
	}
}

// Runs returns recent runs for a routine, newest first. limit defaults to 20
// and is capped at 100.
func (r *Runner) Runs(name string, limit int) ([]RunResult, error) {
	if !validName(name) {
		return nil, ErrNotFound
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	dir := filepath.Join(r.stateDir(name), "runs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	results := make([]RunResult, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validRunID(entry.Name()) {
			continue
		}
		result, err := readResult(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].StartedAt.Equal(results[j].StartedAt) {
			return results[i].ID > results[j].ID
		}
		return results[i].StartedAt.After(results[j].StartedAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// RunInfo returns one run's metadata without reading its log.
func (r *Runner) RunInfo(name, runID string) (RunResult, error) {
	if !validName(name) || !validRunID(runID) {
		return RunResult{}, ErrNotFound
	}
	result, err := readResult(filepath.Join(r.stateDir(name), "runs", runID))
	if err != nil {
		if os.IsNotExist(err) {
			return RunResult{}, ErrNotFound
		}
		return RunResult{}, err
	}
	return result, nil
}

// RunLog returns one run's metadata and log text.
func (r *Runner) RunLog(name, runID string) (RunResult, string, error) {
	result, err := r.RunInfo(name, runID)
	if err != nil {
		return RunResult{}, "", err
	}
	data, err := os.ReadFile(result.Log)
	if err != nil {
		if !os.IsNotExist(err) {
			return RunResult{}, "", err
		}
		data = nil
	}
	return result, string(data), nil
}

// LastRun returns the most recent run metadata for a routine.
func (r *Runner) LastRun(name string) (RunResult, error) {
	runs, err := r.Runs(name, 1)
	if err != nil {
		return RunResult{}, err
	}
	if len(runs) == 0 {
		return RunResult{}, ErrNotFound
	}
	return runs[0], nil
}

func readResult(dir string) (RunResult, error) {
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return RunResult{}, err
	}
	var result RunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func validRunID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func load(dir, name string) (Routine, error) {
	data, err := os.ReadFile(filepath.Join(dir, "routine.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return Routine{}, nil
		}
		return Routine{}, fmt.Errorf("routine %s: %w", name, err)
	}
	var manifest Manifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return Routine{}, fmt.Errorf("routine %s: %w", name, err)
	}
	manifest.Entry = strings.TrimSpace(manifest.Entry)
	if manifest.Entry == "" {
		return Routine{}, fmt.Errorf("routine %s: entry is required", name)
	}
	if filepath.IsAbs(manifest.Entry) || filepath.Clean(manifest.Entry) == ".." ||
		strings.HasPrefix(filepath.Clean(manifest.Entry), ".."+string(os.PathSeparator)) {
		return Routine{}, fmt.Errorf("routine %s: entry must stay inside the routine directory", name)
	}
	keepRuns := defaultKeepRuns
	if manifest.KeepRuns != nil {
		keepRuns = *manifest.KeepRuns
		if keepRuns < 0 || keepRuns > maxKeepRuns {
			return Routine{}, fmt.Errorf("routine %s: keep_runs must be between 0 and %d", name, maxKeepRuns)
		}
	}
	timeout := 10 * time.Minute
	if strings.TrimSpace(manifest.Timeout) != "" {
		timeout, err = time.ParseDuration(strings.TrimSpace(manifest.Timeout))
		if err != nil || timeout <= 0 || timeout > time.Hour {
			return Routine{}, fmt.Errorf("routine %s: timeout must be between 1s and 1h", name)
		}
	}
	location := time.Local
	if strings.TrimSpace(manifest.Timezone) != "" {
		location, err = time.LoadLocation(strings.TrimSpace(manifest.Timezone))
		if err != nil {
			return Routine{}, fmt.Errorf("routine %s: timezone: %w", name, err)
		}
	}
	var schedule *Schedule
	if strings.TrimSpace(manifest.Schedule) != "" {
		parsed, err := ParseSchedule(manifest.Schedule)
		if err != nil {
			return Routine{}, fmt.Errorf("routine %s: %w", name, err)
		}
		schedule = &parsed
	}
	return Routine{
		Name:        name,
		Dir:         dir,
		Description: strings.TrimSpace(manifest.Description),
		Entry:       manifest.Entry,
		Schedule:    schedule,
		Location:    location,
		Timeout:     timeout,
		KeepRuns:    keepRuns,
		Enabled:     manifest.Enabled,
	}, nil
}

func entryPath(item Routine) (string, error) {
	path := filepath.Join(item.Dir, filepath.Clean(item.Entry))
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("routine %s: entry: %w", item.Name, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("routine %s: entry %s is not executable", item.Name, item.Entry)
	}
	return path, nil
}

func (r *Runner) stateDir(name string) string {
	state := r.Paths.State
	if state == "" {
		state = filepath.Join(r.dataRoot(), "state")
	}
	return filepath.Join(state, "routines", name)
}

func (r *Runner) dataRoot() string {
	if r.Paths.DataRoot != "" {
		return r.Paths.DataRoot
	}
	return r.Paths.Root
}

func (r *Runner) workspace() string {
	if r.Paths.Workspace != "" {
		return r.Paths.Workspace
	}
	return r.Paths.Root
}

func (r *Runner) writeResult(dir string, result RunResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".run-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, "run.json"))
}

func runID() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(buf), nil
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func envWith(base []string, kv ...string) []string {
	values := make(map[string]string, len(base)+len(kv)/2)
	order := make([]string, 0, len(base))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = value
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if _, seen := values[kv[i]]; !seen {
			order = append(order, kv[i])
		}
		values[kv[i]] = kv[i+1]
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}
