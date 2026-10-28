package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
)

const defaultInstallURL = "https://github.com/3-lines-studio/ax/releases/latest/download/install.sh"

type botConfig struct {
	Model       string            `toml:"model"`
	BaseURL     string            `toml:"base_url"`
	Tools       []string          `toml:"tools"`
	Consumer    string            `toml:"consumer"`
	Env         map[string]string `toml:"env"`
	PreRun      []preRunHook      `toml:"pre_run"`
	Boti        botiConfig        `toml:"boti"`
	Connections []connectionEntry `toml:"connection"`
}

// connectionEntry is only what the host needs from a [[connection]]: where the
// provider lives and how to start it. The engine owns the rest.
type connectionEntry struct {
	Kind    string `toml:"kind"`
	Command string `toml:"command"`
}

// botiConfig is bot.toml's [boti] table, the host's own configuration. Botdir
// reserves root keys for the specification, so the loader keeps its settings in
// a table named after itself.
type botiConfig struct {
	Serve serveConfig `toml:"serve"`
}

type preRunHook struct {
	Command string `toml:"command"`
}

func main() {
	fs := flag.NewFlagSet("boti", flag.ExitOnError)
	dir := fs.String("C", "", "bot root directory (default: $BOT_ROOT, else cwd)")
	installURL := fs.String("install-url", "", "installer script URL (default: $BOTI_INSTALL_URL, else the hosted installer)")
	lines := fs.Int("n", 40, "lines to show (logs)")
	asJSON := fs.Bool("json", false, "print the supervisor state as JSON (status)")
	repo := fs.String("repo", "", "source checkout to build (promote; default [boti.serve].repo)")
	now := fs.Bool("now", false, "activate without waiting for idle (promote)")
	_ = fs.Parse(os.Args[1:])

	root := resolveRoot(*dir)
	mode := "run"
	if args := fs.Args(); len(args) > 0 {
		mode = args[0]
	}

	switch mode {
	case "install":
		if err := install(root, installerURL(*installURL)); err != nil {
			fatal("install: %v", err)
		}
	case "run":
		if err := run(root); err != nil {
			fatal("run: %v", err)
		}
	case "selftest":
		if err := selftest(root); err != nil {
			fatal("selftest: %v", err)
		}
	case "update":
		if err := update(root, installerURL(*installURL)); err != nil {
			fatal("update: %v", err)
		}
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		if err := runServe(ctx, root); err != nil {
			fatal("serve: %v", err)
		}
	case "status":
		if err := status(root, *asJSON); err != nil {
			fatal("%v", err)
		}
	case "logs":
		if err := logs(root, *lines); err != nil {
			fatal("logs: %v", err)
		}
	case "restart":
		if err := restart(root); err != nil {
			fatal("%v", err)
		}
	case "promote":
		if err := promote(root, promoteOptions{Repo: *repo, Now: *now}); err != nil {
			fatal("promote: %v", err)
		}
	case "rollback":
		if err := rollback(root); err != nil {
			fatal("rollback: %v", err)
		}
	default:
		fatal("unknown mode %q (want install, run, selftest, update, serve, status, logs, restart, promote or rollback)", mode)
	}
}

func resolveRoot(dir string) string {
	root := dir
	if root == "" {
		root = os.Getenv("BOT_ROOT")
	}
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatal("working directory: %v", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		fatal("bot root: %v", err)
	}
	return abs
}

func installerURL(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("BOTI_INSTALL_URL"); v != "" {
		return v
	}
	return defaultInstallURL
}

func install(root, installURL string) error {
	bc, found, err := readBotConfig(root)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("bot.toml is required in %s", root)
	}

	pkgs := []string{"ax", "slaxi"}
	seen := map[string]bool{"ax": true, "slaxi": true}
	for _, t := range bc.Tools {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		pkgs = append(pkgs, t)
	}

	var missing []string
	for _, p := range pkgs {
		if _, err := exec.LookPath(p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	installer, err := fetchInstaller(installURL)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(installer) }()

	// One call: the release is one archive per platform, so naming every
	// missing program still costs a single download.
	fmt.Fprintf(os.Stderr, "boti: installing %s\n", strings.Join(missing, " "))
	if err := runInstaller(installer, missing...); err != nil {
		return fmt.Errorf("install %s: %w", strings.Join(missing, " "), err)
	}
	return nil
}

func run(root string) error {
	if err := os.Chdir(root); err != nil {
		return fmt.Errorf("chdir %s: %w", root, err)
	}

	env := os.Environ()
	env = setEnv(env, "BOT_ROOT", root)
	env = setEnvIfUnset(env, "BOT_DATA", root)

	bc, _, err := readBotConfig(root)
	if err != nil {
		return err
	}
	for k, v := range bc.Env {
		env = setEnvIfUnset(env, k, v)
	}
	if err := runHooks(bc, env, os.Stdout, os.Stderr); err != nil {
		return err
	}

	consumer := bc.Consumer
	if consumer == "" {
		consumer = "slaxi"
	}
	consumerPath, err := exec.LookPath(consumer)
	if err != nil {
		return fmt.Errorf("find consumer %s: %w", consumer, err)
	}
	return syscall.Exec(consumerPath, []string{consumer}, env)
}

func readBotConfig(root string) (botConfig, bool, error) {
	var bc botConfig
	if !botdir.Exists(filepath.Join(root, "bot.toml")) {
		return bc, false, nil
	}
	if err := botdir.LoadTOML(root, &bc); err != nil {
		return bc, true, err
	}
	return bc, true, nil
}

var installerClient = &http.Client{Timeout: 30 * time.Second}

func fetchInstaller(url string) (string, error) {
	var data []byte
	switch {
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"):
		resp, err := installerClient.Get(url)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("installer %s: %s", url, resp.Status)
		}
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
	case strings.HasPrefix(url, "file://"):
		d, err := os.ReadFile(strings.TrimPrefix(url, "file://"))
		if err != nil {
			return "", err
		}
		data = d
	default:
		return "", fmt.Errorf("unsupported installer URL %q", url)
	}

	f, err := os.CreateTemp("", "boti-install-*.sh")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func runInstaller(script string, pkgs ...string) error {
	cmd := exec.Command("sh", append([]string{script}, pkgs...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func setEnvIfUnset(env []string, key, value string) []string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):]
		}
	}
	return ""
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "boti: "+format+"\n", args...)
	os.Exit(1)
}

// botPackages is what a bot needs on this host: the engine, its default
// transport and every tool the bot lists.
func botPackages(root string) ([]string, error) {
	bc, found, err := readBotConfig(root)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("bot.toml is required in %s", root)
	}
	pkgs := []string{"ax", "slaxi"}
	seen := map[string]bool{"ax": true, "slaxi": true}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || strings.ContainsRune(name, os.PathSeparator) {
			return
		}
		seen[name] = true
		pkgs = append(pkgs, name)
	}
	// A bot that names its own consumer or transport needs that program too,
	// even when it is not a tool provider.
	if bc.Consumer != "" {
		add(bc.Consumer)
	}
	if len(bc.Boti.Serve.Command) > 0 {
		add(bc.Boti.Serve.Command[0])
	}
	// Connections are the unit: a local provider is a program the host has to
	// install and check, exactly like a legacy tools entry.
	for _, connection := range bc.Connections {
		kind := strings.ToLower(strings.TrimSpace(connection.Kind))
		if kind != "" && kind != "local" {
			continue
		}
		add(connection.Command)
	}
	for _, t := range bc.Tools {
		add(t)
	}
	return pkgs, nil
}

// selftest answers "can this host run this bot": every program is on PATH and
// the engine can parse the bot, resolve its connections and load its catalog.
func selftest(root string) error {
	pkgs, err := botPackages(root)
	if err != nil {
		return err
	}
	var missing []string
	for _, pkg := range pkgs {
		path, err := exec.LookPath(pkg)
		if err != nil {
			missing = append(missing, pkg)
			fmt.Fprintf(os.Stderr, "fail: %s is not on PATH\n", pkg)
			continue
		}
		fmt.Fprintf(os.Stderr, "ok:   %s %s\n", pkg, path)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing programs: %s (run boti install)", strings.Join(missing, " "))
	}
	axPath, err := exec.LookPath("ax")
	if err != nil {
		return err
	}
	cmd := exec.Command(axPath, "-C", root, "selftest")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ax selftest: %w", err)
	}
	return nil
}

// backup is one installed binary kept next to nothing: enough to put the host
// back the way it was when a new build does not survive its selftest.
type backup struct {
	path string
	data []byte
	mode os.FileMode
}

// update installs the current release over what is here, then checks the new
// engine. If the check fails, the previous binaries come back and the error
// says so: a bad release must not leave a host that cannot start.
func update(root, installerURL string) error {
	pkgs, err := botPackages(root)
	if err != nil {
		return err
	}
	var backups []backup
	for _, pkg := range pkgs {
		path, err := exec.LookPath(pkg)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("backup %s: %w", path, err)
		}
		backups = append(backups, backup{path: path, data: data, mode: info.Mode()})
	}

	installer, err := fetchInstaller(installerURL)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(installer) }()
	if err := runInstaller(installer, append([]string{"update"}, pkgs...)...); err != nil {
		return fmt.Errorf("update: %w", err)
	}

	if err := selftest(root); err != nil {
		if restoreErr := restore(backups); restoreErr != nil {
			return fmt.Errorf("the new build failed its selftest (%v) and the rollback failed too: %w", err, restoreErr)
		}
		return fmt.Errorf("the new build failed its selftest, previous binaries restored: %w", err)
	}
	fmt.Fprintf(os.Stderr, "boti: updated %s\n", strings.Join(pkgs, " "))
	return nil
}

func restore(backups []backup) error {
	for _, item := range backups {
		dir := filepath.Dir(item.path)
		temporary, err := os.CreateTemp(dir, ".boti-restore-*")
		if err != nil {
			return err
		}
		name := temporary.Name()
		if _, err := temporary.Write(item.data); err != nil {
			_ = temporary.Close()
			_ = os.Remove(name)
			return err
		}
		if err := temporary.Close(); err != nil {
			_ = os.Remove(name)
			return err
		}
		if err := os.Chmod(name, item.mode); err != nil {
			_ = os.Remove(name)
			return err
		}
		if err := os.Rename(name, item.path); err != nil {
			_ = os.Remove(name)
			return err
		}
		fmt.Fprintf(os.Stderr, "boti: restored %s\n", item.path)
	}
	return nil
}
