package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) (*store, string) {
	t.Helper()
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &store{roots: []*os.Root{root}}, rootPath
}

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestListAndReadSkills(t *testing.T) {
	skills, root := testStore(t)
	writeSkill(t, root, "second", "Second description\n\nSecond body")
	writeSkill(t, root, "first", "---\ndescription: First description\n---\nFirst body")
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "- first: First description\n- second: Second description\n" {
		t.Fatalf("unexpected list: %q", list)
	}
	content, err := skills.read("first")
	if err != nil {
		t.Fatal(err)
	}
	if content != "First body" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestRejectsInvalidNamesAndEscapes(t *testing.T) {
	skills, root := testStore(t)
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.read("../outside"); err == nil {
		t.Fatal("expected invalid name error")
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Dir(outside), link); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.read("linked"); err == nil {
		t.Fatal("expected symlink escape error")
	}
}

func TestParseFallback(t *testing.T) {
	description, content := parse("\nTitle\nBody")
	if description != "Title" || content != "\nTitle\nBody" {
		t.Fatalf("unexpected parse result: %q %q", description, content)
	}
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"", "Upper", "bad_name", "-bad", "bad-", "bad--name"} {
		if validateName(name) == nil {
			t.Fatalf("accepted invalid name: %q", name)
		}
	}
	if err := validateName("bigquery-metrics"); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRootsAndProjectPrecedence(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("BOT_ROOT", "")
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "shared", "Project skill")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "shared", "Home skill")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "home-only", "Home only")
	skills, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer skills.close()
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "- home-only: Home only\n- shared: Project skill\n" {
		t.Fatalf("unexpected list: %q", list)
	}
	content, err := skills.read("shared")
	if err != nil {
		t.Fatal(err)
	}
	if content != "Project skill" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestMissingDefaultRootsAreEmpty(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BOT_ROOT", "")
	skills, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer skills.close()
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "No skills installed." {
		t.Fatalf("unexpected list: %q", list)
	}
}

func TestDescriptors(t *testing.T) {
	if len(descriptors) != 2 || !strings.Contains(descriptors[0], `"name":"skills"`) || !strings.Contains(descriptors[1], `"name":"skill"`) {
		t.Fatal("invalid descriptors")
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("SKILLX_TEST_CLI") == "1" {
		os.Args = append([]string{"skillx"}, strings.Fields(os.Getenv("SKILLX_TEST_CLI_ARGS"))...)
		main()
		return
	}
	os.Exit(m.Run())
}

func cliEnv(home, botRoot string, args []string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BOT_ROOT=") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "SKILLX_TEST_CLI=1", "SKILLX_TEST_CLI_ARGS="+strings.Join(args, " "), "BOT_ROOT="+botRoot, "HOME="+home)
	return env
}

func runCLI(t *testing.T, home, botRoot, stdin string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = cliEnv(home, botRoot, args)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err == nil {
		return out.String(), errOut.String(), 0
	} else if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), errOut.String(), ee.ExitCode()
	} else {
		t.Fatalf("failed to run CLI: %v", err)
		return "", "", -1
	}
}

func TestCLIDescribe(t *testing.T) {
	stdout, stderr, code := runCLI(t, t.TempDir(), "", "", "describe")
	if code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, `"name":"skills"`) || !strings.Contains(stdout, `"name":"skill"`) {
		t.Fatalf("unexpected descriptors: %q", stdout)
	}
}

func TestCLIRunSkillsRejectsName(t *testing.T) {
	_, stderr, code := runCLI(t, t.TempDir(), "", `{"name":"x"}`, "run", "skills")
	if code != 2 || !strings.Contains(stderr, "name is not accepted") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestCLIRunSkillRequiresName(t *testing.T) {
	_, stderr, code := runCLI(t, t.TempDir(), "", `{}`, "run", "skill")
	if code != 2 || !strings.Contains(stderr, "name is required") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestCLIRunSkillNotFound(t *testing.T) {
	_, stderr, code := runCLI(t, t.TempDir(), "", `{"name":"nope"}`, "run", "skill")
	if code != 1 || !strings.Contains(stderr, "skill not found: nope") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestCLIUsageAndUnknownTool(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		args   []string
		stdin  string
		stderr string
	}{
		{[]string{"run"}, "{}", "usage: skillx"},
		{[]string{}, "{}", "usage: skillx"},
		{[]string{"bogus"}, "{}", "usage: skillx"},
		{[]string{"run", "wat"}, "{}", "unknown tool: wat"},
	} {
		_, stderr, code := runCLI(t, home, "", tc.stdin, tc.args...)
		if code != 2 || !strings.Contains(stderr, tc.stderr) {
			t.Fatalf("args %v: exit %d stderr %q", tc.args, code, stderr)
		}
	}
}

func TestCLIInputValidation(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		name   string
		stdin  string
		stderr string
	}{
		{"unknown field", `{"name":"z","extra":1}`, "unknown field"},
		{"trailing json", `{"name":"z"} {}`, "expected one JSON object"},
		{"array input", `[1,2]`, "cannot unmarshal"},
	} {
		_, stderr, code := runCLI(t, home, "", tc.stdin, "run", "skills")
		if code != 2 || !strings.Contains(stderr, tc.stderr) {
			t.Fatalf("%s: exit %d stderr %q", tc.name, code, stderr)
		}
	}
}

func TestCLIInputTooLarge(t *testing.T) {
	_, stderr, code := runCLI(t, t.TempDir(), "", strings.Repeat("x", maxInput+1), "run", "skills")
	if code != 2 || !strings.Contains(stderr, "input exceeds 16 KiB") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestCLIListAndReadSkills(t *testing.T) {
	bot := t.TempDir()
	writeSkill(t, filepath.Join(bot, "skills"), "alpha", "---\ndescription: Alpha skill\n---\nAlpha body")
	writeSkill(t, filepath.Join(bot, "skills"), "beta", "Beta description\nBeta body")
	stdout, stderr, code := runCLI(t, t.TempDir(), bot, `{}`, "run", "skills")
	if code != 0 {
		t.Fatalf("list exit %d stderr %q", code, stderr)
	}
	if stdout != "- alpha: Alpha skill\n- beta: Beta description\n" {
		t.Fatalf("unexpected list: %q", stdout)
	}
	content, stderr, code := runCLI(t, t.TempDir(), bot, `{"name":"alpha"}`, "run", "skill")
	if code != 0 {
		t.Fatalf("read exit %d stderr %q", code, stderr)
	}
	if content != "Alpha body" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestCLIDoubleDashStripped(t *testing.T) {
	stdout, stderr, code := runCLI(t, t.TempDir(), "", "{}", "--", "run", "skills")
	if code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if stdout != "No skills installed." {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
}

func TestCLIOpenStoreErrorFails(t *testing.T) {
	bot := t.TempDir()
	if err := os.WriteFile(filepath.Join(bot, "skills"), []byte("not dir"), 0600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, t.TempDir(), bot, "{}", "run", "skills")
	if code != 1 || stderr == "" {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestBotRootSkillsTakePrecedence(t *testing.T) {
	bot := t.TempDir()
	project := t.TempDir()
	home := t.TempDir()
	writeSkill(t, filepath.Join(bot, "skills"), "root-sk", "Root skill")
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "proj-sk", "Project skill")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "home-sk", "Home skill")
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("BOT_ROOT", bot)
	skills, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer skills.close()
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "- root-sk: Root skill\n" {
		t.Fatalf("unexpected list: %q", list)
	}
	if _, err := skills.read("proj-sk"); err == nil {
		t.Fatal("expected project skill to be excluded")
	}
}

func TestBotRootWithoutSkillsFallsThrough(t *testing.T) {
	bot := t.TempDir()
	project := t.TempDir()
	home := t.TempDir()
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "proj-sk", "Project skill")
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("BOT_ROOT", bot)
	skills, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer skills.close()
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "- proj-sk: Project skill\n" {
		t.Fatalf("unexpected list: %q", list)
	}
}

func TestBotRootSkillsFileFails(t *testing.T) {
	bot := t.TempDir()
	if err := os.WriteFile(filepath.Join(bot, "skills"), []byte("not a dir"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOT_ROOT", bot)
	t.Setenv("HOME", t.TempDir())
	if _, err := openStore(); err == nil {
		t.Fatal("expected error opening skills file")
	}
}

func TestProjectSkillsDirTakesPrecedenceOverLegacy(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("BOT_ROOT", "")
	writeSkill(t, filepath.Join(project, "skills"), "modern", "Modern skill")
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "legacy", "Legacy skill")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "home-sk", "Home skill")
	skills, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer skills.close()
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "- home-sk: Home skill\n- modern: Modern skill\n" {
		t.Fatalf("unexpected list: %q", list)
	}
	if _, err := skills.read("legacy"); err == nil {
		t.Fatal("expected legacy skill to be excluded when ./skills exists")
	}
}

func TestMultiRootPrecedenceForDuplicates(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	openA, err := os.OpenRoot(rootA)
	if err != nil {
		t.Fatal(err)
	}
	openB, err := os.OpenRoot(rootB)
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(t, rootA, "shared", "From A")
	writeSkill(t, rootB, "shared", "From B")
	writeSkill(t, rootB, "only-b", "Only B")
	skills := &store{roots: []*os.Root{openA, openB}}
	t.Cleanup(skills.close)
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "- only-b: Only B\n- shared: From A\n" {
		t.Fatalf("unexpected list: %q", list)
	}
	content, err := skills.read("shared")
	if err != nil {
		t.Fatal(err)
	}
	if content != "From A" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestListSkipsNonSkills(t *testing.T) {
	skills, root := testStore(t)
	if err := os.WriteFile(filepath.Join(root, "plain-file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nodoc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Uppercase"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, root, "empty-desc", "---\ndescription:\n---\n")
	writeSkill(t, root, "valid", "A description\nA body")
	list, err := skills.list()
	if err != nil {
		t.Fatal(err)
	}
	if list != "- valid: A description\n" {
		t.Fatalf("unexpected list: %q", list)
	}
}

func TestReadEmptyBodyReturnsMessage(t *testing.T) {
	skills, root := testStore(t)
	writeSkill(t, root, "bare", "---\ndescription: Has desc\n---\n")
	content, err := skills.read("bare")
	if err != nil {
		t.Fatal(err)
	}
	if content != "Skill is empty." {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestReadErrors(t *testing.T) {
	skills, _ := testStore(t)
	if _, err := skills.read("nope"); err == nil || !strings.Contains(err.Error(), "skill not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := skills.read(""); err == nil || !strings.Contains(err.Error(), "invalid skill name") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := skills.read("../escape"); err == nil {
		t.Fatal("expected traversal error")
	}
}

func TestValidateNameBoundaries(t *testing.T) {
	if err := validateName("a"); err != nil {
		t.Fatalf("single char rejected: %v", err)
	}
	if err := validateName(strings.Repeat("a", 64)); err != nil {
		t.Fatalf("64-char name rejected: %v", err)
	}
	if err := validateName(strings.Repeat("a", 65)); err == nil {
		t.Fatal("accepted 65-char name")
	}
	for _, name := range []string{"..", ".", "a/b", `a\b`, "café", "--", "-", "a.b"} {
		if validateName(name) == nil {
			t.Fatalf("accepted invalid name: %q", name)
		}
	}
}

func TestParseFrontmatterVariants(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		description string
		body        string
	}{
		{"standard", "---\ndescription: My skill\n---\nThe body", "My skill", "The body"},
		{"trim leading newlines", "---\ndescription: A\n---\n\n\nBody", "A", "Body"},
		{"no description falls back to first body line", "---\n---\nFirst body line\nrest", "First body line", "First body line\nrest"},
		{"spaced value", "---\ndescription:   padded   \n---\nBody", "padded", "Body"},
		{"plain fallback", "Description here\nbody", "Description here", "Description here\nbody"},
		{"leading blank", "\n\nTitle\nbody", "Title", "\n\nTitle\nbody"},
		{"malformed no close", "---\ndescription: A\nbody remains", "---", "---\ndescription: A\nbody remains"},
		{"empty content", "", "", ""},
	} {
		description, body := parse(tc.content)
		if description != tc.description || body != tc.body {
			t.Fatalf("%s: got (%q, %q) want (%q, %q)", tc.name, description, body, tc.description, tc.body)
		}
	}
}
