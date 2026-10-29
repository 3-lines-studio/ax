package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestServiceUnit(t *testing.T) {
	t.Setenv("BOT_DATA", "")
	spec := serviceSpec{Webi: "/home/u/.local/bin/webi", Ax: "/home/u/.local/bin/ax", Root: "/home/u/bots/me", Listen: "0.0.0.0:8377"}
	unit := serviceUnit(spec)
	for _, want := range []string{
		"WorkingDirectory=/home/u/bots/me",
		"ExecStart=/home/u/.local/bin/webi -C /home/u/bots/me -listen 0.0.0.0:8377 -ax /home/u/.local/bin/ax -open=false",
		"Environment=PATH=/home/u/.local/bin:/home/u/.local/bin:/usr/local/bin:/usr/bin:/bin",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	withState := serviceUnit(serviceSpec{Webi: "/w", Ax: "/a", Root: "/r", State: "/s", Listen: "127.0.0.1:8377"})
	if !strings.Contains(withState, "-state /s") {
		t.Fatalf("state flag missing:\n%s", withState)
	}
}

func TestLaunchAgent(t *testing.T) {
	t.Setenv("BOT_DATA", "")
	plist := launchAgent(serviceSpec{Webi: "/w", Ax: "/a", Root: "/r", State: "/s", Listen: "127.0.0.1:8377"})
	for _, want := range []string{
		"<string>" + launchLabel + "</string>",
		"<string>-C</string><string>/r</string>",
		"<string>-listen</string><string>127.0.0.1:8377</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<string>/s/webi.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
}

func TestSetupWithoutSystemd(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := runSetup(setupOptions{Root: t.TempDir(), Listen: "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "systemctl") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunSetup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd unit test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BOT_DATA", "")
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"$CALLS\"\n" +
		"case \"$1 $2\" in\n" +
		"  '--user is-active') exit 1 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ax"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CALLS", calls)
	t.Setenv("USER", "tester")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	root := filepath.Join(home, "bots", "me")
	if err := runSetup(setupOptions{Root: root, Listen: "127.0.0.1:" + strconv.Itoa(port), AXPath: "ax"}); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", serviceName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "ExecStart="+strconv.Quote(os.Args[0])) && !strings.Contains(string(unit), "ExecStart="+os.Args[0]) {
		t.Fatalf("exec start missing from unit:\n%s", unit)
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--user daemon-reload", "--user enable --now " + serviceName} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("systemctl calls missing %q:\n%s", want, log)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "state", "webi", "token")); err != nil {
		t.Fatal(err)
	}
}

func TestAskListenNonInteractive(t *testing.T) {
	if isTerminal(os.Stdin) || isTerminal(os.Stdout) {
		t.Skip("interactive terminal")
	}
	if got := askListen(); got != "127.0.0.1:8377" {
		t.Fatalf("askListen = %q", got)
	}
}
