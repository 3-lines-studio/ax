package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// starterBot is written when webi creates a bot directory, so tools get a
// workspace and the file is a hint about what else a bot can hold.
const starterBot = `# A Botdir: bot.md adds context, skills/ adds procedures,
# workspace/ is where tools work. Any OpenAI-compatible endpoint works.
model = "deepseek-flash"
`

// starterProviders are the local tools a fresh bot gets when they are
// installed: without them the first conversation has no hands.
var starterProviders = []string{"fsx", "bashx", "skillx", "memoryx"}

func writeStarter(root string) error {
	var starter strings.Builder
	starter.WriteString(starterBot)
	for _, name := range starterProviders {
		if _, err := exec.LookPath(name); err != nil {
			continue
		}
		fmt.Fprintf(&starter, "\n[[connection]]\nid = %q\nkind = \"local\"\ncommand = %q\n", name, name)
	}
	return os.WriteFile(filepath.Join(root, "bot.toml"), []byte(starter.String()), 0o644)
}

// announce prints the one link a first visit needs. It stays off the log: the
// supervisor's log is a file, and a token in it would outlive the terminal.
// Only a person at a terminal sees (and opens) the link.
func announce(listener net.Listener, token string, open bool) {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return
	}
	host := ""
	if !addr.IP.IsUnspecified() {
		host = addr.IP.String()
	}
	printURLs(host, addr.Port, token, open, "")
}

// printURLs is shared by the foreground server and `webi setup`.
func printURLs(host string, port int, token string, open bool, tokenPath string) {
	if !isTerminal(os.Stdout) {
		if tokenPath != "" {
			fmt.Fprintf(os.Stdout, "token: %s\n", tokenPath)
		}
		return
	}
	localHost := host
	all := false
	if localHost == "" || localHost == "0.0.0.0" || localHost == "::" {
		localHost = "localhost"
		all = true
	} else if localHost == "127.0.0.1" || localHost == "::1" {
		localHost = "localhost"
	}
	local := "http://" + net.JoinHostPort(localHost, strconv.Itoa(port)) + "/?token=" + token
	fmt.Fprintf(os.Stdout, "\n  webi is ready\n\n  This machine:  %s\n", local)
	if all {
		if lan := lanAddress(); lan != "" {
			fmt.Fprintf(os.Stdout, "  Other devices: http://%s/?token=%s\n", net.JoinHostPort(lan, strconv.Itoa(port)), token)
		}
	}
	fmt.Fprintln(os.Stdout, "\n  The link sets a cookie, so it is needed only once. Treat it like the PIN.")
	if open {
		openBrowser(local)
	}
}

// alreadyServes reports whether a webi with this token is already answering on
// the address. Typing webi again then opens the running app instead of failing
// with "address already in use".
func alreadyServes(listen, token string) bool {
	host, port := splitListen(listen)
	if port == 0 {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort(host, strconv.Itoa(port))+"/api/state", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode == http.StatusOK
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
