package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// starterBot is written when webi creates a bot directory, so tools get a
// workspace and the file is a hint about what else a bot can hold.
const starterBot = `# A Botdir: bot.md adds context, skills/ adds procedures,
# workspace/ is where tools work. Any OpenAI-compatible endpoint works.
model = "deepseek-flash"
`

// resolveRoot picks the bot directory. Running from the home directory is the
// common first case, and using $HOME as a workspace would be a bad surprise,
// so that one case lands in ~/bots/default. Everything else is the current
// directory, and an explicit -C always wins.
func resolveRoot(explicit string) (root string, fresh bool, err error) {
	if explicit == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", false, err
		}
		root = cwd
		if home, err := os.UserHomeDir(); err == nil && filepath.Clean(cwd) == filepath.Clean(home) {
			root = filepath.Join(home, "bots", "default")
		}
	} else {
		root = expandHome(explicit)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(root)
	if err == nil {
		if !info.IsDir() {
			return "", false, fmt.Errorf("%s is not a directory", root)
		}
		return root, false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", false, err
	}
	return root, true, nil
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func writeStarter(root string) error {
	return os.WriteFile(filepath.Join(root, "bot.toml"), []byte(starterBot), 0o644)
}

// announce prints the one link a first visit needs. It stays off the log: the
// supervisor's log is a file, and a token in it would outlive the terminal.
// Only a person at a terminal sees (and opens) the link.
func announce(listener net.Listener, token string, open bool) {
	if !isTerminal(os.Stdout) {
		return
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return
	}
	localHost := addr.IP.String()
	if addr.IP.IsUnspecified() {
		localHost = "127.0.0.1"
	}
	local := "http://" + net.JoinHostPort(localHost, strconv.Itoa(addr.Port)) + "/?token=" + token
	fmt.Fprintf(os.Stdout, "\n  webi is ready\n\n  This machine:  %s\n", local)
	if addr.IP.IsUnspecified() {
		if lan := lanAddress(); lan != "" {
			fmt.Fprintf(os.Stdout, "  Other devices: http://%s/?token=%s\n", net.JoinHostPort(lan, strconv.Itoa(addr.Port)), token)
		}
	}
	fmt.Fprintln(os.Stdout, "\n  The link sets a cookie, so it is needed only once. Treat it like the PIN.")
	if open {
		openBrowser(local)
	}
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// lanAddress is the address to type on a phone: the first private IPv4, or a
// global one if there is no private address.
func lanAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, addr := range addrs {
		network, ok := addr.(*net.IPNet)
		if !ok || network.IP.IsLoopback() || network.IP.IsLinkLocalUnicast() {
			continue
		}
		ip := network.IP.To4()
		if ip == nil {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
		if fallback == "" {
			fallback = ip.String()
		}
	}
	return fallback
}

func openBrowser(url string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return
	}
	go func() { _ = command.Wait() }()
}
