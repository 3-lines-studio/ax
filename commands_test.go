package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExpandPromptCommandNotFound(t *testing.T) {
	root := t.TempDir()
	if got := expandPromptCommand("hello world", root); got != "hello world" {
		t.Errorf("non-command = %q", got)
	}
	if got := expandPromptCommand("/missing cmd", root); got != "/missing cmd" {
		t.Errorf("unknown command = %q", got)
	}
}

func TestExpandPromptCommandExpands(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "commands"), 0o755)
	os.WriteFile(filepath.Join(root, "commands", "greet.md"), []byte("---\ndescription: greet\n---\nHello $1 $2"), 0o644)
	got := expandPromptCommand("/greet Alice Bob", root)
	if got != "Hello Alice Bob" {
		t.Errorf("expanded = %q", got)
	}
}

func TestExpandPromptCommandQuotedArg(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "commands"), 0o755)
	os.WriteFile(filepath.Join(root, "commands", "greet.md"), []byte("Hello $1"), 0o644)
	got := expandPromptCommand("/greet \"Alice Bob\"", root)
	if got != "Hello Alice Bob" {
		t.Errorf("quoted = %q", got)
	}
}

func TestExpandUserCommandNoRestReturnsContent(t *testing.T) {
	uc := userCommand{name: "x", content: "static"}
	if got := expandUserCommand(uc, ""); got != "static" {
		t.Errorf("no rest = %q", got)
	}
}

func TestExpandUserCommandAppendsRestWhenNoPlaceholders(t *testing.T) {
	uc := userCommand{name: "x", content: "static"}
	if got := expandUserCommand(uc, "extra args"); got != "static\n\nextra args" {
		t.Errorf("appended = %q", got)
	}
}

func TestSubstituteArgsFull(t *testing.T) {
	args := []string{"one", "two", "three"}
	cases := []struct {
		in, want string
	}{
		{"$1", "one"},
		{"$2 $3", "two three"},
		{"$9", ""},
		{"$@", "one two three"},
		{"$ARGUMENTS", "one two three"},
		{"${2:-default}", "two"},
		{"${9:-default}", "default"},
		{"${1:-default} and ${9:-x}", "one and x"},
		{"${@:1}", "one two three"},
		{"${@:2}", "two three"},
		{"${@:3}", "three"},
		{"${@:4}", ""},
		{"${@:2:1}", "two"},
		{"${@:1:2}", "one two"},
		{"count is $1 and rest ${@:2}", "count is one and rest two three"},
	}
	for _, c := range cases {
		if got := substituteArgs(c.in, args); got != c.want {
			t.Errorf("substituteArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSubstituteArgsEmptyArgs(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"$1", ""},
		{"$@", ""},
		{"$ARGUMENTS", ""},
		{"${2:-fallback}", "fallback"},
		{"${@:1}", ""},
		{"${@:1:2}", ""},
	}
	for _, c := range cases {
		if got := substituteArgs(c.in, nil); got != c.want {
			t.Errorf("substituteArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseCommandArgsQuotes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a 'b c' d", []string{"a", "b c", "d"}},
		{"'single'", []string{"single"}},
		{"", []string{}},
		{`"a b" c`, []string{"a b", "c"}},
	}
	for _, c := range cases {
		if got := parseCommandArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseCommandArgs(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLoadUserCommandsFiltersAndSorts(t *testing.T) {
	root := t.TempDir()
	cmds := filepath.Join(root, "commands")
	os.MkdirAll(filepath.Join(cmds, "sub"), 0o755)
	os.WriteFile(filepath.Join(cmds, "b.md"), []byte("---\ndescription: bee\n---\nbody b"), 0o644)
	os.WriteFile(filepath.Join(cmds, "a.md"), []byte("body a"), 0o644)
	os.WriteFile(filepath.Join(cmds, "ignore.txt"), []byte("x"), 0o644)

	got := loadUserCommands(root)
	if len(got) != 2 {
		t.Fatalf("got %d commands, want 2", len(got))
	}
	if got[0].name != "a" || got[1].name != "b" {
		t.Errorf("names = %q %q", got[0].name, got[1].name)
	}
	if got[0].content != "body a" {
		t.Errorf("a content = %q", got[0].content)
	}
	if got[1].description != "bee" {
		t.Errorf("b description = %q", got[1].description)
	}
}

func TestParsePlaceholderOutOfRange(t *testing.T) {
	tok, consumed := parsePlaceholder("$7 rest", []string{"one"}, "one")
	if tok != "" || consumed != 2 {
		t.Errorf("tok=%q consumed=%d", tok, consumed)
	}
}

func TestExpandBracedDefault(t *testing.T) {
	tok, _ := expandBraced("2:-set", []string{"one"}, "one")
	if tok != "set" {
		t.Errorf("2:-set = %q", tok)
	}
}

func TestIsASCIIDigitAndAlnum(t *testing.T) {
	if !isASCIIDigit('5') || isASCIIDigit('a') {
		t.Error("digit check failed")
	}
	if !isASCIIAlnum('5') || !isASCIIAlnum('a') || !isASCIIAlnum('Z') || isASCIIAlnum('-') || isASCIIAlnum('_') {
		t.Error("alnum check failed")
	}
}
