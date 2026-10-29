package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
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

const (
	serviceName = "webi.service"
	launchLabel = "studio.3-lines.ax.webi"
)

type setupOptions struct {
	Root   string
	State  string
	Listen string
	AXPath string
	Open   bool
	Wait   time.Duration
}

type serviceSpec struct {
	Webi   string
	Ax     string
	Root   string
	State  string
	Listen string
}

func (s serviceSpec) args() []string {
	args := []string{s.Webi, "-C", s.Root, "-listen", s.Listen, "-ax", s.Ax, "-open=false"}
	if s.State != "" {
		args = append(args, "-state", s.State)
	}
	return args
}

func runSetup(opts setupOptions) error {
	if err := checkAutostart(); err != nil {
		return err
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
	listen := opts.Listen
	if listen == "" {
		listen = askListen()
	}
	spec := serviceSpec{Webi: self, Ax: ax, Root: root, State: state, Listen: listen}
	active, changed, err := installAutostart(spec)
	if err != nil {
		return err
	}
	if opts.Wait > 0 && (!active || changed) && !waitListening(listen, opts.Wait) {
		return fmt.Errorf("the service did not answer on %s; check: %s", listen, statusHint())
	}
	host, port := splitListen(listen)
	printURLs(host, port, token, opts.Open, tokenPath)
	if runtime.GOOS == "linux" && !active {
		hintLinger()
	}
	return nil
}

func askListen() string {
	input := os.Stdin
	if !isTerminal(input) && isTerminal(os.Stdout) {
		if tty, err := os.Open("/dev/tty"); err == nil {
			defer func() { _ = tty.Close() }()
			input = tty
		}
	}
	if !isTerminal(input) {
		fmt.Fprintln(os.Stderr, " Local only (run webi setup in a terminal to share it with other devices).")
		return "127.0.0.1:8377"
	}
	fmt.Fprint(os.Stdout, "\n Use it from your phone or another computer on this network? [y/N] ")
	line, _ := bufio.NewReader(input).ReadString('\n')
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
		return "0.0.0.0:8377"
	}
	return "127.0.0.1:8377"
}

func checkAutostart() error {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
			return fmt.Errorf("systemctl not found: start webi in a terminal instead")
		}
	case "darwin":
		if _, err := exec.LookPath("launchctl"); err != nil {
			return fmt.Errorf("launchctl not found: start webi in a terminal instead")
		}
	default:
		return fmt.Errorf("automatic start is not available on %s; run webi at login", runtime.GOOS)
	}
	return nil
}

func installAutostart(spec serviceSpec) (active, changed bool, err error) {
	switch runtime.GOOS {
	case "linux":
		return installSystemd(spec)
	case "darwin":
		return installLaunchd(spec)
	default:
		return false, false, fmt.Errorf("automatic start is not available on %s; run webi at login", runtime.GOOS)
	}
}

func installSystemd(spec serviceSpec) (active, changed bool, err error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, false, fmt.Errorf("systemctl not found: start webi in a terminal instead")
	}
	changed, err = installUnit(serviceUnit(spec))
	if err != nil {
		return false, false, err
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return false, false, fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	active = exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName).Run() == nil
	if active {
		if changed {
			if out, err := exec.Command("systemctl", "--user", "restart", serviceName).CombinedOutput(); err != nil {
				return true, changed, fmt.Errorf("systemctl restart %s: %v: %s", serviceName, err, strings.TrimSpace(string(out)))
			}
		}
		return true, changed, nil
	}
	if portInUse(spec.Listen) {
		return false, changed, fmt.Errorf("%s is already in use; stop the service that owns it, then run webi setup again", spec.Listen)
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", serviceName).CombinedOutput(); err != nil {
		return false, changed, fmt.Errorf("systemctl enable --now %s: %v: %s", serviceName, err, strings.TrimSpace(string(out)))
	}
	return false, changed, nil
}

func installLaunchd(spec serviceSpec) (active, changed bool, err error) {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return false, false, fmt.Errorf("launchctl not found: start webi in a terminal instead")
	}
	path, err := launchAgentPath()
	if err != nil {
		return false, false, err
	}
	changed, err = installFile(path, launchAgent(spec), 0o644)
	if err != nil {
		return false, false, err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	active = exec.Command("launchctl", "print", domain+"/"+launchLabel).Run() == nil
	if active && changed {
		_ = exec.Command("launchctl", "bootout", domain+"/"+launchLabel).Run()
		active = false
	}
	if !active {
		if portInUse(spec.Listen) {
			return false, changed, fmt.Errorf("%s is already in use; stop the service that owns it, then run webi setup again", spec.Listen)
		}
		if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
			return false, changed, fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return active, changed, nil
}

func installUnit(unit string) (bool, error) {
	dir, err := userConfigDir()
	if err != nil {
		return false, err
	}
	return installFile(filepath.Join(dir, "systemd", "user", serviceName), unit, 0o644)
}

func installFile(path, content string, mode os.FileMode) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(content), mode); err != nil {
		return false, err
	}
	return true, nil
}

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}

func serviceUnit(spec serviceSpec) string {
	args := make([]string, 0, len(spec.args()))
	for _, arg := range spec.args() {
		args = append(args, systemdArg(arg))
	}
	env := "Environment=PATH=" + systemdArg(filepath.Dir(spec.Webi)+":"+filepath.Dir(spec.Ax)+":/usr/local/bin:/usr/bin:/bin")
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
`, systemdArg(spec.Root), strings.Join(args, " "), env)
}

func launchAgent(spec serviceSpec) string {
	var args bytes.Buffer
	for _, arg := range spec.args() {
		fmt.Fprintf(&args, "<string>%s</string>", xmlEscape(arg))
	}
	env := fmt.Sprintf("<key>PATH</key><string>%s</string>", xmlEscape(filepath.Dir(spec.Webi)+":"+filepath.Dir(spec.Ax)+":/usr/local/bin:/usr/bin:/bin"))
	if data := os.Getenv("BOT_DATA"); data != "" {
		env += fmt.Sprintf("<key>BOT_DATA</key><string>%s</string>", xmlEscape(data))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
 <key>Label</key><string>%s</string>
 <key>ProgramArguments</key><array>%s</array>
 <key>WorkingDirectory</key><string>%s</string>
 <key>RunAtLoad</key><true/>
 <key>KeepAlive</key><true/>
 <key>ThrottleInterval</key><integer>5</integer>
 <key>StandardOutPath</key><string>%s</string>
 <key>StandardErrorPath</key><string>%s</string>
 <key>EnvironmentVariables</key><dict>%s</dict>
</dict>
</plist>
`, launchLabel, args.String(), xmlEscape(spec.Root), xmlEscape(filepath.Join(spec.State, "webi.log")), xmlEscape(filepath.Join(spec.State, "webi.log")), env)
}

func xmlEscape(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
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

func statusHint() string {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("launchctl print gui/%d/%s", os.Getuid(), launchLabel)
	default:
		return "systemctl --user status " + serviceName
	}
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
	fmt.Fprintf(os.Stderr, "\n To keep it running after you log out: loginctl enable-linger %s\n", account)
}
