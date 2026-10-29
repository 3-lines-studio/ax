package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type promoteOptions struct {
	Repo string
	Now  bool
}

const (
	buildTimeout   = 20 * time.Minute
	testTimeout    = 20 * time.Minute
	selftestLimit  = 2 * time.Minute
	defaultKeep    = 3
	buildDirPrefix = ".stage-"
)

func promote(root string, paths hostPaths, opts promoteOptions) error {
	bc, found, err := readBotConfig(root)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("bot.toml is required")
	}
	cfg := bc.serveSettings()
	repo := firstNonEmpty(opts.Repo, cfg.Repo)
	if repo == "" {
		return errors.New("set [boti.serve] repo to the ax checkout")
	}
	repo, err = filepath.Abs(repo)
	if err != nil {
		return err
	}
	if _, err := controlStatus(paths.sock); err != nil {
		return fmt.Errorf("`boti serve` is not running in %s: %w", root, err)
	}
	if err := os.MkdirAll(paths.builds, 0o755); err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(paths.builds, ".lock"), "another `boti promote` is running for this bot")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	rev, dirty, err := gitInfo(repo)
	if err != nil {
		return err
	}
	programs, err := buildPrograms(root, repo)
	if err != nil {
		return err
	}
	id := rev
	if dirty {
		id += "-dirty"
	}
	id = uniqueBuild(paths.builds, id)

	fmt.Fprintf(os.Stderr, "boti: building %s from %s\n", id, strings.Join(programs, " "))
	if err := runStep(repo, testTimeout, "go", "vet", "./..."); err != nil {
		return fmt.Errorf("go vet: %w", err)
	}
	if err := runStep(repo, testTimeout, "go", "test", "./..."); err != nil {
		return fmt.Errorf("go test: %w", err)
	}

	stage := filepath.Join(paths.builds, buildDirPrefix+id)
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	args := append([]string{filepath.Join(repo, "scripts", "package.sh"), "--dest", stage}, programs...)
	if err := runStep(repo, buildTimeout, "sh", args...); err != nil {
		return fmt.Errorf("package.sh: %w", err)
	}
	binDir := filepath.Join(paths.builds, id, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := installBuild(stage, binDir, buildDonors(paths.builds)); err != nil {
		return err
	}

	meta := buildMeta{ID: id, Rev: rev, Dirty: dirty, Created: time.Now().UTC(), Programs: programs}
	if err := writeMeta(filepath.Join(paths.builds, id, "meta.json"), meta); err != nil {
		return err
	}
	if err := validateBuild(root, bc, cfg, id, binDir); err != nil {
		return fmt.Errorf("build %s did not validate: %w", id, err)
	}
	fmt.Fprintf(os.Stderr, "boti: build %s passed its checks\n", id)

	when := "now"
	if !opts.Now && cfg.Idle != "" {
		when = "idle"
	}
	reply, err := activate(paths.sock, id, when)
	if err != nil {
		return err
	}
	switch {
	case reply.Pending:
		fmt.Fprintf(os.Stderr, "boti: prepared %s; activates when the bot is idle\n", id)
	case reply.Activated:
		fmt.Fprintf(os.Stderr, "boti: activated %s\n", id)
	default:
		fmt.Fprintf(os.Stderr, "boti: supervisor answered with %+v\n", reply)
	}
	return nil
}

func rollback(root string, paths hostPaths) error {
	reply, err := controlPost(paths.sock, "/rollback", nil)
	if err != nil {
		return err
	}
	var out struct {
		Build string `json:"build"`
	}
	if err := json.Unmarshal(reply, &out); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "boti: rolled back to %s\n", out.Build)
	return nil
}

func activate(sock, build, when string) (activateReply, error) {
	body, _ := json.Marshal(map[string]string{"build": build, "when": when})
	data, err := controlPost(sock, "/activate", body)
	if err != nil {
		return activateReply{}, err
	}
	var reply activateReply
	if err := json.Unmarshal(data, &reply); err != nil {
		return activateReply{}, err
	}
	return reply, nil
}

type activateReply struct {
	Build     string `json:"build"`
	Activated bool   `json:"activated"`
	Pending   bool   `json:"pending"`
}

func gitInfo(repo string) (string, bool, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--short=7", "HEAD").Output()
	if err != nil {
		return "", false, fmt.Errorf("%s is not a git checkout: %w", repo, err)
	}
	rev := strings.TrimSpace(string(out))
	if rev == "" {
		return "", false, fmt.Errorf("%s: git returned no revision", repo)
	}
	status, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if err != nil {
		return "", false, err
	}
	return rev, strings.TrimSpace(string(status)) != "", nil
}

func buildPrograms(root, repo string) ([]string, error) {
	pkgs, err := botPackages(root)
	if err != nil {
		return nil, err
	}
	var out []string
	hasAX := false
	for _, pkg := range pkgs {
		if info, err := os.Stat(filepath.Join(repo, "cmd", pkg)); err != nil || !info.IsDir() {
			continue
		}
		out = append(out, pkg)
		if pkg == "ax" {
			hasAX = true
		}
	}
	if !hasAX {
		return nil, fmt.Errorf("%s has no cmd/ax; is [boti.serve].repo an ax checkout?", repo)
	}
	return out, nil
}

func uniqueBuild(builds, id string) string {
	candidate := id
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(builds, candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", id, n)
	}
}

func installBuild(stage, binDir string, donors []string) error {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(stage, entry.Name())
		dst := filepath.Join(binDir, entry.Name())
		if err := placeFile(src, dst, donors); err != nil {
			return err
		}
	}
	return nil
}

func placeFile(src, dst string, donors []string) error {
	for _, dir := range donors {
		donor := filepath.Join(dir, filepath.Base(dst))
		same, err := sameContent(src, donor)
		if err == nil && same {
			if err := os.Link(donor, dst); err == nil {
				_ = os.Remove(src)
				return nil
			}
		}
	}
	return os.Rename(src, dst)
}

func sameContent(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if ai.Size() != bi.Size() {
		return false, nil
	}
	left, err := fileHash(a)
	if err != nil {
		return false, err
	}
	right, err := fileHash(b)
	if err != nil {
		return false, err
	}
	return left == right, nil
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func buildDonors(builds string) []string {
	var donors []string
	if id := currentBuild(builds); id != "" {
		donors = append(donors, filepath.Join(builds, id, "bin"))
	}
	return donors
}

func currentBuild(builds string) string {
	target, err := os.Readlink(filepath.Join(builds, "current"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

type buildMeta struct {
	ID       string    `json:"id"`
	Rev      string    `json:"rev"`
	Dirty    bool      `json:"dirty,omitempty"`
	Created  time.Time `json:"created"`
	Programs []string  `json:"programs"`
}

func writeMeta(path string, meta buildMeta) error {
	data, err := json.MarshalIndent(meta, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

func readMeta(path string) buildMeta {
	var meta buildMeta
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	return meta
}

func validateBuild(root string, bc botConfig, cfg serveConfig, id, binDir string) error {
	paths := newHostPaths(root)
	env := hostEnv(paths, bc)
	env = setEnv(env, "AX_BUILD", id)
	env = setEnv(env, "PATH", prependPath(binDir, envValue(env, "PATH")))

	ax, err := lookPath("ax", envValue(env, "PATH"))
	if err != nil {
		return err
	}
	if err := runStepEnv(root, env, selftestLimit, ax, "selftest", "-C", root); err != nil {
		return fmt.Errorf("ax selftest: %w", err)
	}
	if len(cfg.Selftest) == 0 {
		return nil
	}
	name, err := lookPath(cfg.Selftest[0], envValue(env, "PATH"))
	if err != nil {
		return err
	}
	if err := runStepEnv(root, env, selftestLimit, name, cfg.Selftest[1:]...); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(cfg.Selftest, " "), err)
	}
	return nil
}

func prependPath(dir, path string) string {
	if path == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + path
}

func cleanEnv(env []string) []string {
	drop := map[string]bool{
		"BOT_ROOT":         true,
		"BOT_DATA":         true,
		"AX_TOOLS":         true,
		"DEEPSEEK_API_KEY": true,
		"OPENAI_API_KEY":   true,
		"XDG_CONFIG_HOME":  true,
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[key] {
			out = append(out, entry)
		}
	}
	return out
}

func runStep(dir string, timeout time.Duration, name string, args ...string) error {
	return runStepEnv(dir, cleanEnv(os.Environ()), timeout, name, args...)
}

func runStepEnv(dir string, env []string, timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if text := strings.TrimSpace(out.String()); text != "" {
			fmt.Fprintln(os.Stderr, text)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("timed out after %s", timeout)
		}
		return err
	}
	return nil
}

func (h *host) pruneBuilds(keep int) {
	entries, err := os.ReadDir(h.paths.builds)
	if err != nil {
		return
	}
	h.mu.Lock()
	current, previous := h.state.Build, h.state.Previous
	h.mu.Unlock()
	type candidate struct {
		id      string
		created time.Time
	}
	var others []candidate
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == current || entry.Name() == previous {
			continue
		}
		created := readMeta(filepath.Join(h.paths.builds, entry.Name(), "meta.json")).Created
		if created.IsZero() {
			if info, err := entry.Info(); err == nil {
				created = info.ModTime()
			}
		}
		others = append(others, candidate{id: entry.Name(), created: created})
	}
	sort.Slice(others, func(i, j int) bool { return others[i].created.After(others[j].created) })
	limit := keep - 2
	if limit < 1 {
		limit = 1
	}
	for i, item := range others {
		if i < limit {
			continue
		}
		_ = os.RemoveAll(filepath.Join(h.paths.builds, item.id))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
