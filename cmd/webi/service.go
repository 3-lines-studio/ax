package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
)

const serviceName = "webi.service"

type setupOptions struct {
	Root   string
	State  string
	Listen string
	AXPath string
	Open   bool
	Wait   time.Duration
}

// runSetup installs a systemd user unit for this webi and starts it. It is the
// one-time step that turns the command into an app: after it, the UI is at the
// printed URL and comes back with the session.
func runSetup(opts setupOptions) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("webi setup installs a systemd user unit; on %s start webi in a terminal or add it to your login items", runtime.GOOS)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found: start webi in a terminal instead")
	}
	root, fresh, err := resolveRoot(opts.Root)
	if err != nil {
		return err
	}
	if fresh {
		if err := writeStarter(root); err != nil {
			return err
		}
	}
	paths := botdir.Resolve(root)
	state := opts.State
	if state == "" {
		state = filepath.Join(paths.DataRoot, "state", "webi")
	}
	if state, err = filepath.Abs(state); err != nil {
		return err
	}
	if _, err := NewStore(state); err != nil {
		return err
	}
	token, tokenPath, err := loadToken(state)
	if err != nil {
		return err
	}
	// A token pinned with WEBI_TOKEN exists only in the environment; the
	// service needs a copy, so keep the same secret in the usual file.
	if os.Getenv("WEBI_TOKEN") != "" {
		tokenPath = filepath.Join(state, "token")
		if err := writeFileAtomic(tokenPath, []byte(token+"\n"), 0o600); err != nil {
			return err
		}
	}
	ax, err := exec.LookPath(opts.AXPath)
	if err != nil {
		return fmt.Errorf("cannot find ax (%s); install it or pass -ax", opts.AXPath)
	}
	if ax, err = filepath.Abs(ax); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	changed, err := installUnit(serviceUnit(self, ax, root, state, opts.Listen))
	if err != nil {
		return err
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	active := exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName).Run() == nil
	if !active {
		if portInUse(opts.Listen) {
			return fmt.Errorf("%s is already in use; stop the service that owns it, then run webi setup again", opts.Listen)
		}
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", serviceName).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable --now %s: %v: %s", serviceName, err, strings.TrimSpace(string(out)))
		}
		if opts.Wait > 0 && !waitListening(opts.Listen, opts.Wait) {
			return fmt.Errorf("the service did not answer on %s; check: systemctl --user status %s", opts.Listen, serviceName)
		}
	} else if changed {
		fmt.Fprintf(os.Stderr, "unit updated; restart it with: systemctl --user restart %s\n", serviceName)
	}
	host, port := splitListen(opts.Listen)
	printURLs(host, port, token, opts.Open, tokenPath)
	if !active {
		hintLinger()
	}
	return nil
}

// installUnit reports whether the unit changed, so a running service can be
// told to restart instead of silently keeping the old flags.
func installUnit(unit string) (bool, error) {
	dir, err := userConfigDir()
	if err != nil {
		return false, err
	}
	dir = filepath.Join(dir, "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	path := filepath.Join(dir, serviceName)
	if old, err := os.ReadFile(path); err == nil && string(old) == unit {
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(unit), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func serviceUnit(webi, ax, root, state, listen string) string {
	args := []string{systemdArg(webi), "-C", systemdArg(root), "-listen", systemdArg(listen), "-ax", systemdArg(ax), "-open=false"}
	if state != "" {
		args = append(args, "-state", systemdArg(state))
	}
	env := "Environment=PATH=" + systemdArg(filepath.Dir(webi)+":"+filepath.Dir(ax)+":/usr/local/bin:/usr/bin:/bin")
	if data := os.Getenv("BOT_DATA"); data != "" {
		env += "\nEnvironment=BOT_DATA=" + systemdArg(data)
	}
	return fmt.Sprintf(`[Unit]
Description=ax web UI (webi)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s
%s
Restart=on-failure
RestartSec=2
KillSignal=SIGTERM
TimeoutStopSec=120

[Install]
WantedBy=default.target
`, systemdArg(root), strings.Join(args, " "), env)
}

func systemdArg(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\"\\") {
		return value
	}
	return strconv.Quote(value)
}

func userConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

func splitListen(addr string) (host string, port int) {
	host, raw, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 8377
	}
	port, err = strconv.Atoi(raw)
	if err != nil {
		return host, 8377
	}
	return host, port
}

func waitListening(addr string, timeout time.Duration) bool {
	host, port := splitListen(addr)
	if port == 0 {
		return true
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func portInUse(addr string) bool {
	host, port := splitListen(addr)
	if port == 0 {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func hintLinger() {
	account := os.Getenv("USER")
	if account == "" {
		if current, err := user.Current(); err == nil {
			account = current.Username
		}
	}
	if account == "" {
		return
	}
	out, err := exec.Command("loginctl", "show-user", account, "-p", "Linger", "--value").Output()
	if err == nil && strings.TrimSpace(string(out)) == "yes" {
		return
	}
	fmt.Fprintf(os.Stderr, "\n  To keep it running after you log out: loginctl enable-linger %s\n", account)
}
