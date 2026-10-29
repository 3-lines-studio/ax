package botdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv("BOT_ROOT")
	_ = os.Unsetenv("BOT_DATA")
	os.Exit(m.Run())
}

type testConfig struct {
	Model   string   `toml:"model"`
	BaseURL string   `toml:"base_url"`
	Tools   []string `toml:"tools"`
}

func TestLoadTOMLReadsKeys(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte("model = \"gpt-4\"\nbase_url = \"http://bot\"\ntools = [\"read\", \"edit\"]\n"), 0o644)
	var bc testConfig
	if err := LoadTOML(root, &bc); err != nil {
		t.Fatal(err)
	}
	if bc.Model != "gpt-4" || bc.BaseURL != "http://bot" {
		t.Errorf("bc = %+v", bc)
	}
	if len(bc.Tools) != 2 || bc.Tools[0] != "read" || bc.Tools[1] != "edit" {
		t.Errorf("tools = %v", bc.Tools)
	}
}

func TestLoadTOMLMissingFile(t *testing.T) {
	var bc testConfig
	if err := LoadTOML(t.TempDir(), &bc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bc.Model != "" || bc.BaseURL != "" || len(bc.Tools) != 0 {
		t.Errorf("bc = %+v", bc)
	}
}

func TestLoadTOMLMalformed(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte("model ==\n"), 0o644)
	var bc testConfig
	if err := LoadTOML(root, &bc); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadTOMLErrorWrapped(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "bot.toml"), 0o755)
	var bc testConfig
	err := LoadTOML(root, &bc)
	if err == nil || !strings.Contains(err.Error(), "read bot.toml") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveNoBot(t *testing.T) {
	root := t.TempDir()
	ctx := Resolve(root)
	if ctx.IsBot {
		t.Error("IsBot should be false")
	}
	if ctx.Workspace != root {
		t.Errorf("Workspace = %q, want root", ctx.Workspace)
	}
}

func TestResolveWithWorkspaceDir(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "workspace"), 0o755)
	ctx := Resolve(root)
	if !ctx.IsBot {
		t.Error("IsBot should be true")
	}
	if ctx.Workspace != filepath.Join(root, "workspace") {
		t.Errorf("Workspace = %q", ctx.Workspace)
	}
}

func TestResolveCreatesWorkspace(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644)
	ctx := Resolve(root)
	want := filepath.Join(root, "workspace")
	if ctx.Workspace != want {
		t.Errorf("Workspace = %q, want %q", ctx.Workspace, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Errorf("workspace not created: %v", err)
	}
}

func TestResolveSkillsAndPrompt(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "skills"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "bot.md"), []byte("  extra context  \n"), 0o644)
	ctx := Resolve(root)
	if ctx.Skills != filepath.Join(root, "skills") {
		t.Errorf("Skills = %q", ctx.Skills)
	}
	if ctx.Prompt != "extra context" {
		t.Errorf("Prompt = %q", ctx.Prompt)
	}
}

func TestResolveAPIKey(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "secrets"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "secrets", "api-key"), []byte("sk-secret\n"), 0o644)
	ctx := Resolve(root)
	if !ctx.HasAPIKey || ctx.APIKey != "sk-secret" {
		t.Errorf("HasAPIKey=%v APIKey=%q", ctx.HasAPIKey, ctx.APIKey)
	}
}

func TestResolveWorkspacePathOccupiedByFile(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(root, "workspace"), []byte("not a dir"), 0o644)
	ctx := Resolve(root)
	if ctx.Workspace != filepath.Join(root, "workspace") {
		t.Errorf("Workspace = %q", ctx.Workspace)
	}
}

func TestResolveUsesBotDataRoot(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "bot.toml"), []byte(""), 0o644)
	t.Setenv("BOT_DATA", data)

	ctx := Resolve(root)
	wantWorkspace := filepath.Join(data, "workspace")
	if ctx.Workspace != wantWorkspace {
		t.Errorf("Workspace = %q, want %q", ctx.Workspace, wantWorkspace)
	}
	if ctx.State != filepath.Join(data, "state") {
		t.Errorf("State = %q, want %q", ctx.State, filepath.Join(data, "state"))
	}
	if _, err := os.Stat(filepath.Join(root, "workspace")); err == nil {
		t.Error("workspace must be created under BOT_DATA, not the bot root")
	}

	_ = os.MkdirAll(filepath.Join(root, "skills"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "bot.md"), []byte("hi"), 0o644)
	ctx = Resolve(root)
	if ctx.Skills != filepath.Join(root, "skills") {
		t.Errorf("Skills = %q, want bot-root skills", ctx.Skills)
	}
	if ctx.Prompt != "hi" {
		t.Errorf("Prompt = %q, want bot-root prompt", ctx.Prompt)
	}
	_ = os.MkdirAll(filepath.Join(root, "routines"), 0o755)
	ctx = Resolve(root)
	if ctx.Routines != filepath.Join(root, "routines") {
		t.Errorf("Routines = %q, want bot-root routines", ctx.Routines)
	}
}

func TestResolveDataIgnoresHostBotData(t *testing.T) {
	host := t.TempDir()
	channel := t.TempDir()
	_ = os.WriteFile(filepath.Join(channel, "bot.toml"), []byte(""), 0o644)
	t.Setenv("BOT_DATA", host)

	ctx := ResolveData(channel, channel)
	if ctx.DataRoot != channel || ctx.State != filepath.Join(channel, "state") {
		t.Errorf("DataRoot = %q State = %q, want the channel root", ctx.DataRoot, ctx.State)
	}
	if ctx.Workspace != filepath.Join(channel, "workspace") {
		t.Errorf("Workspace = %q, want the channel workspace", ctx.Workspace)
	}
	if _, err := os.Stat(filepath.Join(host, "workspace")); err == nil {
		t.Error("host BOT_DATA must not receive the channel workspace")
	}
	if ctx := ResolveData(channel, ""); ctx.DataRoot != channel {
		t.Errorf("empty data root = %q, want the root", ctx.DataRoot)
	}
}

func TestWorkspace(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	t.Setenv("BOT_ROOT", "")
	t.Setenv("BOT_DATA", "")
	t.Chdir(root)

	if got := Workspace(); got != "." {
		t.Errorf("standalone = %q, want .", got)
	}

	_ = os.MkdirAll(filepath.Join(root, "workspace"), 0o755)
	if got := Workspace(); got != filepath.Join(".", "workspace") {
		t.Errorf("standalone workspace = %q", got)
	}

	botRoot := t.TempDir()
	t.Setenv("BOT_ROOT", botRoot)
	if got := Workspace(); got != botRoot {
		t.Errorf("BOT_ROOT without workspace = %q, want %q", got, botRoot)
	}
	_ = os.MkdirAll(filepath.Join(botRoot, "workspace"), 0o755)
	if got := Workspace(); got != filepath.Join(botRoot, "workspace") {
		t.Errorf("BOT_ROOT workspace = %q", got)
	}

	t.Setenv("BOT_DATA", data)
	if got := Workspace(); got != data {
		t.Errorf("BOT_DATA wins = %q, want %q", got, data)
	}
	_ = os.MkdirAll(filepath.Join(data, "workspace"), 0o755)
	if got := Workspace(); got != filepath.Join(data, "workspace") {
		t.Errorf("BOT_DATA workspace = %q", got)
	}
}

func TestReadSecret(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "secrets"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "secrets", "token"), []byte("from-file\n"), 0o600)

	got, err := ReadSecret(root, "AX_TEST_SECRET", "token")
	if err != nil || got != "from-file" {
		t.Errorf("file = %q, %v", got, err)
	}

	t.Setenv("AX_TEST_SECRET", "from-env")
	got, err = ReadSecret(root, "AX_TEST_SECRET", "token")
	if err != nil || got != "from-env" {
		t.Errorf("env = %q, %v", got, err)
	}

	got, err = ReadSecret(root, "AX_TEST_MISSING", "missing")
	if err != nil || got != "" {
		t.Errorf("missing = %q, %v", got, err)
	}

	_ = os.MkdirAll(filepath.Join(root, "secrets", "dir"), 0o755)
	if _, err := ReadSecret(root, "AX_TEST_MISSING", "dir"); err == nil {
		t.Error("expected read error for a directory")
	}
}

func TestExistsAndIsDir(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	if !Exists(file) {
		t.Error("Exists should be true")
	}
	if Exists(filepath.Join(root, "missing")) {
		t.Error("Exists should be false")
	}
	if IsDir(file) {
		t.Error("file is not dir")
	}
	if !IsDir(root) {
		t.Error("root is dir")
	}
}
