package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgsSeparateValues(t *testing.T) {
	cfg, rest, err := parseArgs([]string{"-base", "http://x", "-model", "m1", "-system", "s1", "-C", "dir1", "--session", "sess.json"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.base != "http://x" || cfg.model != "m1" || cfg.system != "s1" || cfg.dir != "dir1" || cfg.session != "sess.json" {
		t.Errorf("cfg = %+v", cfg)
	}
	if len(rest) != 0 {
		t.Errorf("rest = %v", rest)
	}
}

func TestParseArgsInlineValues(t *testing.T) {
	cfg, _, err := parseArgs([]string{"-base=http://y", "-model=m2", "--system=s2", "-C=d2", "--session=s2.json"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.base != "http://y" || cfg.model != "m2" || cfg.system != "s2" || cfg.dir != "d2" || cfg.session != "s2.json" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestParseArgsDoubleDashTerminates(t *testing.T) {
	cfg, rest, err := parseArgs([]string{"-model", "m", "--", "literal", "-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.model != "m" {
		t.Errorf("cfg.model = %q", cfg.model)
	}
	if len(rest) != 2 || rest[0] != "literal" || rest[1] != "-flag" {
		t.Errorf("rest = %v", rest)
	}
}

func TestParseArgsFreeArgsStopParsing(t *testing.T) {
	cfg, rest, err := parseArgs([]string{"-model", "m", "hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.model != "m" {
		t.Errorf("cfg.model = %q", cfg.model)
	}
	if len(rest) != 2 || rest[0] != "hello" || rest[1] != "world" {
		t.Errorf("rest = %v", rest)
	}
}

func TestParseArgsUnknownFlag(t *testing.T) {
	_, _, err := parseArgs([]string{"-nope"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("err = %v", err)
	}
}

func TestParseArgsMissingValue(t *testing.T) {
	_, _, err := parseArgs([]string{"-model"})
	if err == nil || !strings.Contains(err.Error(), "flag needs an argument") {
		t.Errorf("err = %v", err)
	}
}

func TestParseArgsEmptyArgStops(t *testing.T) {
	_, rest, err := parseArgs([]string{"-model", "m", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0] != "" {
		t.Errorf("rest = %v", rest)
	}
}

func TestParseArgsEventsFlagSkipped(t *testing.T) {
	cfg, rest, err := parseArgs([]string{"--events", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0] != "hi" {
		t.Errorf("rest = %v", rest)
	}
	if cfg.base == "hi" {
		t.Errorf("events not consumed as base")
	}
}

func TestParseArgsNoArgs(t *testing.T) {
	cfg, rest, err := parseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (cliConfig{}) || len(rest) != 0 {
		t.Errorf("cfg = %+v rest = %v", cfg, rest)
	}
}

func TestParseArgsSingleDashLongEquals(t *testing.T) {
	cfg, _, err := parseArgs([]string{"-system=inline-sys"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.system != "inline-sys" {
		t.Errorf("cfg.system = %q", cfg.system)
	}
}

func TestBotRootArgForms(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-C", "dir"}, "dir"},
		{[]string{"-C=dir"}, "dir"},
		{[]string{"--C", "dir"}, "dir"},
		{[]string{"--C=dir"}, "dir"},
		{[]string{"-model", "m", "-C", "d"}, "d"},
		{[]string{"-base", "u", "-C", "d"}, "d"},
		{[]string{"-system", "s", "-C", "d"}, "d"},
		{[]string{"-C"}, ""},
		{[]string{"prompt", "-C", "d"}, ""},
		{[]string{"-model", "m", "-C"}, ""},
		{[]string{"--", "-C", "d"}, ""},
		{[]string{}, ""},
		{[]string{"-base=http"}, ""},
	}
	for _, c := range cases {
		if got := botRootArg(c.args); got != c.want {
			t.Errorf("botRootArg(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty(); got != "" {
		t.Errorf("firstNonEmpty() = %q", got)
	}
	if got := firstNonEmpty("", "", "x"); got != "x" {
		t.Errorf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("a", "", "x"); got != "a" {
		t.Errorf("firstNonEmpty = %q", got)
	}
}

func TestAPIKeyPrecedence(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	bot := &botCtx{APIKey: "bot-key", HasAPIKey: true}
	fc := fileConfig{apiKey: "fc-key"}

	t.Setenv("OPENAI_API_KEY", "")
	if got := apiKey(fc, bot); got != "bot-key" {
		t.Errorf("env=empty bot=key -> %q", got)
	}

	t.Setenv("OPENAI_API_KEY", "env-key")
	if got := apiKey(fc, bot); got != "env-key" {
		t.Errorf("env set -> %q", got)
	}

	t.Setenv("OPENAI_API_KEY", "")
	if got := apiKey(fc, nil); got != "fc-key" {
		t.Errorf("no bot -> %q", got)
	}
}

func TestLoadConfigParsesLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "ax")
	_ = os.MkdirAll(cfgDir, 0o755)
	content := "# comment\n\napi_key = \"sk-123\"\nmodel = \"gpt-4\"\nbase = \"http://len\"\ntools = \"bash edit\"\n"
	_ = os.WriteFile(filepath.Join(cfgDir, "config"), []byte(content), 0o644)
	t.Setenv("XDG_CONFIG_HOME", dir)

	c := loadConfig()
	if c.apiKey != "sk-123" || c.model != "gpt-4" || c.base != "http://len" || c.tools != "bash edit" {
		t.Errorf("loadConfig = %+v", c)
	}
}

func TestLoadConfigQuotedValues(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "ax"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "ax", "config"), []byte("model=\"quoted-model\"\n"), 0o644)
	t.Setenv("XDG_CONFIG_HOME", dir)
	if c := loadConfig(); c.model != "quoted-model" {
		t.Errorf("model = %q", c.model)
	}
}

func TestLoadConfigMissingReturnsZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	c := loadConfig()
	if c != (fileConfig{}) {
		t.Errorf("loadConfig = %+v", c)
	}
}

func TestConfigPrecedenceChain(t *testing.T) {
	legacyDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(legacyDir, "ax"), 0o755)
	_ = os.WriteFile(filepath.Join(legacyDir, "ax", "config"), []byte("model = \"legacy-model\"\nbase = \"legacy-base\"\n"), 0o644)
	t.Setenv("XDG_CONFIG_HOME", legacyDir)

	botDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(botDir, "bot.toml"), []byte("model = \"bot-model\"\nbase_url = \"bot-base\"\n"), 0o644)
	bc, err := loadBotConfig(botDir)
	if err != nil {
		t.Fatal(err)
	}
	fc := mergeBotConfig(loadConfig(), bc)

	cfg, _, err := parseArgs([]string{"-base", "cli-base"})
	if err != nil {
		t.Fatal(err)
	}
	if got := firstNonEmpty(cfg.base, fc.base, "default-base"); got != "cli-base" {
		t.Errorf("flag base = %q, want cli-base", got)
	}
	if got := firstNonEmpty(cfg.model, fc.model, "default-model"); got != "bot-model" {
		t.Errorf("model should come from bot, got %q", got)
	}

	cfg2, _, _ := parseArgs(nil)
	if got := firstNonEmpty(cfg2.base, fc.base, "default-base"); got != "bot-base" {
		t.Errorf("no flag base = %q, want bot-base", got)
	}
	if got := firstNonEmpty(cfg2.model, fc.model, "default-model"); got != "bot-model" {
		t.Errorf("no flag model = %q, want bot-model", got)
	}
}

func TestConfigPrecedenceDefaultsWhenNothingSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	fc := mergeBotConfig(loadConfig(), botConfig{})
	cfg, _, _ := parseArgs(nil)
	if got := firstNonEmpty(cfg.base, fc.base, "https://api.openai.com/v1"); got != "https://api.openai.com/v1" {
		t.Errorf("base default = %q", got)
	}
	if got := firstNonEmpty(cfg.model, fc.model, "gpt-4.1-mini"); got != "gpt-4.1-mini" {
		t.Errorf("model default = %q", got)
	}
}

func TestLoadConfigSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "ax"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "ax", "config"), []byte("no-equals-line\nmodel = \"m1\"\nunknown_key = \"u\"\n"), 0o644)
	t.Setenv("XDG_CONFIG_HOME", dir)
	c := loadConfig()
	if c.model != "m1" {
		t.Errorf("model = %q", c.model)
	}
	if c.apiKey != "" || c.base != "" || c.tools != "" {
		t.Errorf("unexpected fields = %+v", c)
	}
}
