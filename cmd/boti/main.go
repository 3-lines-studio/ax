package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/3-lines-studio/ax/internal/botdir"
)

const defaultInstallURL = "https://github.com/3-lines-studio/ax/releases/latest/download/install.sh"

type botConfig struct {
	Model    string            `toml:"model"`
	BaseURL  string            `toml:"base_url"`
	Tools    []string          `toml:"tools"`
	Consumer string            `toml:"consumer"`
	Env      map[string]string `toml:"env"`
	PreRun   []preRunHook      `toml:"pre_run"`
}

type preRunHook struct {
	Command string `toml:"command"`
}

func main() {
	fs := flag.NewFlagSet("boti", flag.ExitOnError)
	dir := fs.String("C", "", "bot root directory (default: $BOT_ROOT, else cwd)")
	installURL := fs.String("install-url", "", "installer script URL (default: $BOTI_INSTALL_URL, else the hosted installer)")
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
	default:
		fatal("unknown mode %q (want install or run)", mode)
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
	for _, h := range bc.PreRun {
		if h.Command == "" {
			continue
		}
		cmd := exec.Command("/bin/sh", "-c", h.Command)
		cmd.Env = env
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pre_run %q: %w", h.Command, err)
		}
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

func fetchInstaller(url string) (string, error) {
	var data []byte
	switch {
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"):
		resp, err := http.Get(url)
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
