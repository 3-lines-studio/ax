package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
)

const selftestTimeout = 60 * time.Second

type selftestReport struct {
	failures int
	warnings int
	lines    []string
}

func (r *selftestReport) ok(format string, args ...any) {
	r.lines = append(r.lines, "ok:   "+fmt.Sprintf(format, args...))
}

func (r *selftestReport) warn(format string, args ...any) {
	r.warnings++
	r.lines = append(r.lines, "warn: "+fmt.Sprintf(format, args...))
}

func (r *selftestReport) fail(format string, args ...any) {
	r.failures++
	r.lines = append(r.lines, "fail: "+fmt.Sprintf(format, args...))
}

func runSelftest(root, state, axPath string, stdout, stderr io.Writer) int {
	report := &selftestReport{}
	paths := botdir.Resolve(root)
	if paths.IsBot {
		report.ok("bot %s", paths.Root)
	} else {
		report.warn("no bot.toml in %s; serving the directory as is", paths.Root)
	}
	report.ok("data %s", paths.DataRoot)

	if ui, err := staticRoot(); err != nil {
		report.fail("embedded UI: %v", err)
	} else {
		missing := []string{}
		for _, name := range []string{"index.html", "app.js", "app.css", "manifest.webmanifest", "sw.js"} {
			if _, err := fs.Stat(ui, name); err != nil {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			report.fail("embedded UI is missing %s", strings.Join(missing, ", "))
		} else {
			report.ok("embedded UI")
		}
	}

	store, err := NewStore(state)
	if err != nil {
		report.fail("state %s: %v", state, err)
	} else {
		channels := store.Channels()
		if len(channels) == 0 {
			report.fail("state %s: no channels", state)
		} else {
			report.ok("state %s (%d channel(s))", state, len(channels))
		}
	}

	switch err := checkVault(filepath.Join(state, "vault.json")); {
	case err != nil:
		report.fail("vault: %v", err)
	case fileExists(filepath.Join(state, "vault.json")):
		report.ok("vault header")
	default:
		report.ok("vault (not created yet)")
	}

	if err := checkSettings(filepath.Join(state, "settings.json")); err != nil {
		report.fail("settings: %v", err)
	} else {
		report.ok("settings")
	}

	if output, err := checkAX(paths, axPath); err != nil {
		report.fail("ax selftest (%s): %v", axPath, err)
		for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
			if line != "" {
				report.lines = append(report.lines, " "+line)
			}
		}
	} else {
		warnings := countWarnings(output)
		if warnings > 0 {
			report.warn("ax selftest (%s) passed with %d warning(s)", axPath, warnings)
		} else {
			report.ok("ax selftest (%s)", axPath)
		}
	}

	for _, line := range report.lines {
		fmt.Fprintln(stdout, line)
	}
	if report.failures > 0 {
		fmt.Fprintf(stderr, "\nselftest failed: %d problem(s), %d warning(s)\n", report.failures, report.warnings)
		return 1
	}
	fmt.Fprintf(stdout, "\nselftest passed with %d warning(s)\n", report.warnings)
	return 0
}

func checkVault(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file vaultFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if file.Version != vaultVersion {
		return fmt.Errorf("%s: version %d is not supported", path, file.Version)
	}
	if file.Salt == "" || file.Nonce == "" || file.Data == "" {
		return fmt.Errorf("%s: incomplete header", path)
	}
	return nil
}

func checkSettings(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func checkAX(paths botdir.Paths, axPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), selftestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, axPath, "selftest", "-C", paths.Root)
	cmd.Env = append(os.Environ(),
		"BOT_ROOT="+paths.Root,
		"BOT_DATA="+paths.DataRoot,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("timed out after %s", selftestTimeout)
	}
	return string(output), err
}

func countWarnings(output string) int {
	n := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "warn:") {
			n++
		}
	}
	return n
}
