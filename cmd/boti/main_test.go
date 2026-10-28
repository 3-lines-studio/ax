package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3-lines-studio/ax/internal/botdir"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func contains(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func TestLoadBotConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `model = "deepseek-v4-flash"
tools = ["wax", "bqx"]
`)

	bc, found, err := readBotConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("bot.toml should be found")
	}
	if bc.Model != "deepseek-v4-flash" {
		t.Fatalf("model = %q", bc.Model)
	}
	if fmtTools(bc.Tools) != "wax bqx" {
		t.Fatalf("tools = %v", bc.Tools)
	}
}

func TestLoadBotConfigMissing(t *testing.T) {
	_, found, err := readBotConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected found=false for missing bot.toml")
	}
}

func TestReadBotConfigEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `base_url = "https://api.deepseek.com"
consumer = "slaxi"
[env]
WAX_NO_SANDBOX = "1"

[[pre_run]]
command = "echo hi"
`)
	bc, _, err := readBotConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bc.Env["WAX_NO_SANDBOX"] != "1" {
		t.Fatalf("env = %v", bc.Env)
	}
	if bc.Consumer != "slaxi" {
		t.Fatalf("consumer = %q", bc.Consumer)
	}
	if bc.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("base_url = %q", bc.BaseURL)
	}
	if len(bc.PreRun) != 1 || bc.PreRun[0].Command != "echo hi" {
		t.Fatalf("pre_run = %+v", bc.PreRun)
	}
}

func TestEnvValue(t *testing.T) {
	env := []string{"A=1", "B=two"}
	if envValue(env, "A") != "1" || envValue(env, "B") != "two" || envValue(env, "C") != "" {
		t.Fatalf("envValue wrong: %v", env)
	}
}

func TestSetEnv(t *testing.T) {
	env := []string{"A=1", "B=2"}
	env = setEnv(env, "A", "X")
	if !contains(env, "A=X") {
		t.Fatalf("replace failed: %v", env)
	}
	env = setEnv(env, "C", "3")
	if !contains(env, "C=3") {
		t.Fatalf("append failed: %v", env)
	}
}

func TestSetEnvIfUnset(t *testing.T) {
	env := []string{"A=1"}
	env = setEnvIfUnset(env, "A", "X")
	if !contains(env, "A=1") || contains(env, "A=X") {
		t.Fatalf("must not override existing: %v", env)
	}
	env = setEnvIfUnset(env, "B", "Y")
	if !contains(env, "B=Y") {
		t.Fatalf("must add missing: %v", env)
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax", "bqx"]
`)

	bindir := filepath.Join(t.TempDir(), "bin")
	installer := filepath.Join(t.TempDir(), "install.sh")
	writeFile(t, installer, `#!/bin/sh
set -eu
bindir="${AX_PREFIX:-$HOME/.local}/bin"
mkdir -p "$bindir"
for p in "$@"; do : > "$bindir/$p"; chmod 0755 "$bindir/$p"; done
`)

	t.Setenv("AX_PREFIX", filepath.Dir(bindir))
	t.Setenv("PATH", "/usr/bin:/bin")

	if err := install(dir, "file://"+installer); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"ax", "slaxi", "wax", "bqx"} {
		if _, err := os.Stat(filepath.Join(bindir, p)); err != nil {
			t.Errorf("installer did not create %s: %v", p, err)
		}
	}
}

func TestInstallIdempotentSkipsPresent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax"]
`)

	bindir := t.TempDir()
	for _, p := range []string{"ax", "slaxi"} {
		src := filepath.Join(bindir, p)
		writeFile(t, src, "")
		if err := os.Chmod(src, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installer := filepath.Join(t.TempDir(), "install.sh")
	writeFile(t, installer, `#!/bin/sh
set -eu
bindir="${AX_PREFIX:-$HOME/.local}/bin"
mkdir -p "$bindir"
for p in "$@"; do : > "$bindir/$p"; chmod 0755 "$bindir/$p"; done
`)
	t.Setenv("AX_PREFIX", bindir)
	t.Setenv("PATH", bindir+":/usr/bin:/bin")

	if err := install(dir, "file://"+installer); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bindir, "bin", "wax")); err != nil {
		t.Errorf("wax was not installed: %v", err)
	}
}

func fmtTools(tools []string) string { return strings.Join(tools, " ") }

func TestLoadBotConfigParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), "model = \"unterminated")
	_, found, err := readBotConfig(dir)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !found {
		t.Fatal("parse error must still report found=true")
	}
	if !strings.Contains(err.Error(), "parse bot.toml") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveRootFromBOTRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BOT_ROOT", dir)
	if got := resolveRoot(""); got != dir {
		t.Fatalf("resolveRoot = %q, want %q", got, dir)
	}
}

func TestResolveRootFromCwd(t *testing.T) {
	t.Setenv("BOT_ROOT", "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveRoot(""); got != want {
		t.Fatalf("resolveRoot = %q, want %q", got, want)
	}
}

func TestResolveRootAbsoluteDir(t *testing.T) {
	dir := t.TempDir()
	if got := resolveRoot(dir); got != dir {
		t.Fatalf("resolveRoot = %q, want %q", got, dir)
	}
}

func TestResolveRootRelativeDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveRoot("."); got != want {
		t.Fatalf("resolveRoot = %q, want %q", got, want)
	}
}

func TestInstallerURLFlagWins(t *testing.T) {
	t.Setenv("BOTI_INSTALL_URL", "from-env")
	if got := installerURL("from-flag"); got != "from-flag" {
		t.Fatalf("installerURL = %q, want from-flag", got)
	}
}

func TestInstallerURLFromEnv(t *testing.T) {
	t.Setenv("BOTI_INSTALL_URL", "from-env")
	if got := installerURL(""); got != "from-env" {
		t.Fatalf("installerURL = %q, want from-env", got)
	}
}

func TestInstallerURLDefault(t *testing.T) {
	t.Setenv("BOTI_INSTALL_URL", "")
	if got := installerURL(""); got != defaultInstallURL {
		t.Fatalf("installerURL = %q, want default", got)
	}
}

func TestPathExists(t *testing.T) {
	f := filepath.Join(t.TempDir(), "missing")
	if botdir.Exists(f) {
		t.Fatal("non-existent path must report false")
	}
	writeFile(t, f, "")
	if !botdir.Exists(f) {
		t.Fatal("existing path must report true")
	}
}

func TestFetchInstallerFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "install.sh")
	content := "#!/bin/sh\ntouch fake\n"
	writeFile(t, src, content)
	script, err := fetchInstaller("file://" + src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(script) }()
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("content = %q, want %q", data, content)
	}
	fi, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installer not executable: %v", fi.Mode())
	}
}

func TestFetchInstallerHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#!/bin/sh\n"))
	}))
	defer srv.Close()
	script, err := fetchInstaller(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(script) }()
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestFetchInstallerHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := fetchInstaller(srv.URL); err == nil {
		t.Fatal("expected error for non-200 status")
	}
}

func TestFetchInstallerUnsupportedScheme(t *testing.T) {
	if _, err := fetchInstaller("ftp://example.com/x"); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

func TestFetchInstallerMissingFile(t *testing.T) {
	if _, err := fetchInstaller("file:///no/such/install.sh"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestRunInstaller(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.sh")
	writeFile(t, ok, "#!/bin/sh\nexit 0\n")
	if err := runInstaller(ok, "ax"); err != nil {
		t.Fatalf("success installer: %v", err)
	}
	bad := filepath.Join(dir, "bad.sh")
	writeFile(t, bad, "#!/bin/sh\nexit 4\n")
	if err := runInstaller(bad, "ax"); err == nil {
		t.Fatal("expected error for failing installer")
	}
}

func TestInstallMissingBotConfig(t *testing.T) {
	err := install(t.TempDir(), "file:///x")
	if err == nil {
		t.Fatal("expected error for missing bot.toml")
	}
	if !strings.Contains(err.Error(), "bot.toml is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), "model = \"bad")
	if err := install(dir, "file:///x"); err == nil {
		t.Fatal("expected parse error")
	} else if !strings.Contains(err.Error(), "parse bot.toml") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallOnlyMissingAndDedup(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax", "wax", "  ", "slaxi"]
`)
	present := t.TempDir()
	for _, p := range []string{"ax", "slaxi"} {
		f := filepath.Join(present, p)
		writeFile(t, f, "")
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	record := filepath.Join(t.TempDir(), "record")
	installer := filepath.Join(t.TempDir(), "install.sh")
	writeFile(t, installer, `#!/bin/sh
set -eu
echo "$*" >> "$RECORD_FILE"
bindir="${AX_PREFIX:-$HOME/.local}/bin"
mkdir -p "$bindir"
for p in "$@"; do : > "$bindir/$p"; chmod 0755 "$bindir/$p"; done
`)
	t.Setenv("AX_PREFIX", t.TempDir())
	t.Setenv("RECORD_FILE", record)
	t.Setenv("PATH", present+":/usr/bin:/bin")

	if err := install(dir, "file://"+installer); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "wax" {
		t.Fatalf("installer invoked with %q, want wax", got)
	}
}

// The release is one archive per platform, so boti installs every missing tool
// with one installer invocation instead of one download per tool.
func TestInstallCallsTheInstallerOnce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax", "fsx"]
`)
	present := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	installer := filepath.Join(t.TempDir(), "install.sh")
	writeFile(t, installer, `#!/bin/sh
set -eu
echo "$*" >> "$RECORD_FILE"
bindir="${AX_PREFIX:-$HOME/.local}/bin"
mkdir -p "$bindir"
for p in "$@"; do : > "$bindir/$p"; chmod 0755 "$bindir/$p"; done
`)
	t.Setenv("AX_PREFIX", t.TempDir())
	t.Setenv("RECORD_FILE", record)
	t.Setenv("PATH", present+":/usr/bin:/bin")

	if err := install(dir, "file://"+installer); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("installer ran %d times, want 1: %q", len(lines), data)
	}
	if lines[0] != "ax slaxi wax fsx" {
		t.Fatalf("installer args = %q", lines[0])
	}
}

func TestInstallSkipsFetchWhenNothingMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax"]
`)
	present := t.TempDir()
	for _, p := range []string{"ax", "slaxi", "wax"} {
		f := filepath.Join(present, p)
		writeFile(t, f, "")
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", present+":/usr/bin:/bin")
	if err := install(dir, "file:///non/existent/install.sh"); err != nil {
		t.Fatalf("install must skip fetch when all tools present: %v", err)
	}
}

func TestInstallInstallerError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax"]
`)
	present := t.TempDir()
	for _, p := range []string{"ax", "slaxi"} {
		f := filepath.Join(present, p)
		writeFile(t, f, "")
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installer := filepath.Join(t.TempDir(), "install.sh")
	writeFile(t, installer, "#!/bin/sh\nexit 9\n")
	t.Setenv("PATH", present+":/usr/bin:/bin")
	err := install(dir, "file://"+installer)
	if err == nil {
		t.Fatal("expected installer error")
	}
	if !strings.Contains(err.Error(), "install wax") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallFetchError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax"]
`)
	present := t.TempDir()
	for _, p := range []string{"ax", "slaxi"} {
		f := filepath.Join(present, p)
		writeFile(t, f, "")
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", present+":/usr/bin:/bin")
	if err := install(dir, "file:///no/such/installer.sh"); err == nil {
		t.Fatal("expected fetch error")
	}
}

func TestHelperSub(t *testing.T) {
	mode := os.Getenv("BOTI_HELPER")
	switch mode {
	case "run":
		root := os.Getenv("BOTI_ROOT_ARG")
		if err := run(root); err != nil {
			fmt.Fprintf(os.Stderr, "helper error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	case "main":
		args := strings.Split(os.Getenv("BOTI_MAIN_ARGS"), "\x1f")
		os.Args = append([]string{"boti"}, args...)
		main()
	default:
		t.Skip("helper subprocess")
	}
}

func spawnHelper(t *testing.T, mode, root string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperSub", "-test.paniconexit0=false")
	childEnv := os.Environ()
	add := func(kv string) {
		key, _, _ := strings.Cut(kv, "=")
		for i, e := range childEnv {
			if strings.HasPrefix(e, key+"=") {
				childEnv[i] = kv
				return
			}
		}
		childEnv = append(childEnv, kv)
	}
	add("BOTI_HELPER=" + mode)
	if root != "" {
		add("BOTI_ROOT_ARG=" + root)
	}
	for _, e := range env {
		add(e)
	}
	cmd.Env = childEnv
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("spawn %s helper: %v", mode, err)
		}
	}
	return string(out), code
}

func TestRunWiresEnvAndHooksThenExec(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), `consumer = "cons-echo"
[env]
WAX_NO_SANDBOX = "config"
PRIVATE = "secret"

[[pre_run]]
command = "echo hook:$BOT_ROOT:$BOT_DATA:$WAX_NO_SANDBOX"

[[pre_run]]
command = "echo hook2:$PRIVATE"
`)
	bindir := t.TempDir()
	writeFile(t, filepath.Join(bindir, "cons-echo"), `#!/bin/sh
echo "cons:$(pwd):$BOT_ROOT:$BOT_DATA:$WAX_NO_SANDBOX:$PRIVATE"
exit 7
`)
	t.Setenv("PATH", bindir+":"+os.Getenv("PATH"))
	t.Setenv("BOT_ROOT", "/host-wrong-root")
	t.Setenv("WAX_NO_SANDBOX", "host")

	out, code := spawnHelper(t, "run", root)
	if code != 7 {
		t.Fatalf("exit code = %d, want 7\n%s", code, out)
	}
	// After chdir, the consumer's `pwd` reports the physical path; on macOS a
	// tmpdir under /var/folders resolves to /private/var/folders. Resolve the
	// root so the assertion holds on any platform.
	physicalRoot := root
	if pr, err := filepath.EvalSymlinks(root); err == nil {
		physicalRoot = pr
	}
	for _, want := range []string{
		"hook:" + root + ":" + root + ":host",
		"hook2:secret",
		"cons:" + physicalRoot + ":" + root + ":" + root + ":host:secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestRunPreservesHostBotData(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), `consumer = "cons-echo"
`)
	bindir := t.TempDir()
	writeFile(t, filepath.Join(bindir, "cons-echo"), `#!/bin/sh
echo "cons:$BOT_ROOT:$BOT_DATA"
exit 0
`)
	t.Setenv("PATH", bindir+":"+os.Getenv("PATH"))
	t.Setenv("BOT_DATA", "/host-data")
	out, code := spawnHelper(t, "run", root)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "cons:"+root+":/host-data") {
		t.Fatalf("host BOT_DATA not preserved\n%s", out)
	}
}

func TestRunPreRunOrdering(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), `consumer = "order-cons"

[[pre_run]]
command = "echo A"

[[pre_run]]
command = "echo B"

[[pre_run]]
command = "echo C"
`)
	bindir := t.TempDir()
	writeFile(t, filepath.Join(bindir, "order-cons"), "#!/bin/sh\necho \"CONSUMER:DONE\"\n")
	t.Setenv("PATH", bindir+":"+os.Getenv("PATH"))
	out, code := spawnHelper(t, "run", root)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	idxA := strings.Index(out, "A\n")
	idxB := strings.Index(out, "B\n")
	idxC := strings.Index(out, "C\n")
	idxD := strings.Index(out, "CONSUMER:DONE")
	if idxA < 0 || idxB < 0 || idxC < 0 || idxD < 0 {
		t.Fatalf("output missing tokens\n%s", out)
	}
	if idxA >= idxB || idxB >= idxC || idxC >= idxD {
		t.Fatalf("hooks/consumer out of order: %d %d %d %d", idxA, idxB, idxC, idxD)
	}
}

func TestRunPreRunNonZeroAborts(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	bindir := t.TempDir()
	writeFile(t, filepath.Join(bindir, "abort-cons"), "#!/bin/sh\ntouch \"$MARKER\"\n")
	bot := fmt.Sprintf("consumer = \"abort-cons\"\n[env]\nMARKER = %q\n\n[[pre_run]]\ncommand = \"echo first\"\n\n[[pre_run]]\ncommand = \"exit 3\"\n", marker)
	writeFile(t, filepath.Join(root, "bot.toml"), bot)
	t.Setenv("PATH", bindir+":"+os.Getenv("PATH"))
	out, code := spawnHelper(t, "run", root)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "first") {
		t.Fatalf("first hook should have run\n%s", out)
	}
	if !strings.Contains(out, "pre_run") {
		t.Fatalf("missing pre_run error\n%s", out)
	}
	if botdir.Exists(marker) {
		t.Fatal("consumer must not run when a pre_run hook fails")
	}
}

func TestRunConsumerNotFound(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), `consumer = "no-such-cons"
`)
	bindir := t.TempDir()
	t.Setenv("PATH", bindir+":"+os.Getenv("PATH"))
	out, code := spawnHelper(t, "run", root)
	if code != 1 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "find consumer") {
		t.Fatalf("missing find consumer error\n%s", out)
	}
}

func TestRunDefaultConsumerIsSlaxi(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), `[env]
TRACE = "on"
`)
	bindir := t.TempDir()
	writeFile(t, filepath.Join(bindir, "slaxi"), "#!/bin/sh\necho \"slaxi:$BOT_ROOT:$TRACE\"\nexit 0\n")
	t.Setenv("PATH", bindir+":"+os.Getenv("PATH"))
	out, code := spawnHelper(t, "run", root)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "slaxi:"+root+":on") {
		t.Fatalf("default consumer slaxi not used\n%s", out)
	}
}

func TestRunChdirFails(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist")
	out, code := spawnHelper(t, "run", nonexistent)
	if code != 1 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "chdir") {
		t.Fatalf("missing chdir error\n%s", out)
	}
}

func TestMainUnknownMode(t *testing.T) {
	out, code := spawnHelper(t, "main", "", "BOTI_MAIN_ARGS=bogus")
	if code != 1 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "unknown mode") {
		t.Fatalf("missing unknown mode error\n%s", out)
	}
}

func TestMainInstallSuccess(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bot.toml"), `tools = ["wax"]
`)
	present := t.TempDir()
	for _, p := range []string{"ax", "slaxi", "wax"} {
		f := filepath.Join(present, p)
		writeFile(t, f, "")
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", present+":/usr/bin:/bin")
	args := strings.Join([]string{"-C", dir, "install"}, "\x1f")
	out, code := spawnHelper(t, "main", "", "BOTI_MAIN_ARGS="+args)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
}

func fakeBin(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	writeFile(t, path, body)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSelftestRequiresEveryProgram(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), "tools = [\"fsx\"]\n")
	bin := fakeBin(t, "ax", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	err := selftest(root)
	if err == nil || !strings.Contains(err.Error(), "slaxi") {
		t.Fatalf("err = %v", err)
	}
}

func TestSelftestRunsTheEngine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), "tools = [\"fsx\"]\n")
	record := filepath.Join(t.TempDir(), "args")
	bin := t.TempDir()
	for _, name := range []string{"ax", "slaxi", "fsx"} {
		path := filepath.Join(bin, name)
		if name == "ax" {
			writeFile(t, path, "#!/bin/sh\nprintf '%s' \"$*\" > "+record+"\nexit 0\n")
		} else {
			writeFile(t, path, "#!/bin/sh\nexit 0\n")
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if err := selftest(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-C") || !strings.Contains(string(data), "selftest") {
		t.Fatalf("ax was called with %q", data)
	}
}

// A build that fails its selftest must put the previous binaries back, or a bad
// release leaves a host that cannot start.
func TestUpdateRollsBackOnFailedSelftest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bot.toml"), "tools = []\n")
	bin := fakeBin(t, "ax", "#!/bin/sh\necho old > /dev/null\nexit 0\n")
	for _, name := range []string{"slaxi"} {
		path := filepath.Join(bin, name)
		writeFile(t, path, "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installed := filepath.Join(bin, "ax")
	before, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}

	installer := filepath.Join(t.TempDir(), "install.sh")
	writeFile(t, installer, `#!/bin/sh
set -eu
if [ "${1:-}" != "update" ]; then
  echo "expected update" >&2
  exit 1
fi
cat > "$AX_TARGET" <<'BROKEN'
#!/bin/sh
exit 9
BROKEN
chmod 0755 "$AX_TARGET"
`)
	t.Setenv("AX_TARGET", installed)
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	err = update(root, "file://"+installer)
	if err == nil || !strings.Contains(err.Error(), "previous binaries restored") {
		t.Fatalf("err = %v", err)
	}
	after, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("ax was not restored:\nbefore=%q\nafter=%q", before, after)
	}
}
