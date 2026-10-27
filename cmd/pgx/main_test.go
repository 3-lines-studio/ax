package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestValidateReadOnly(t *testing.T) {
	for _, sql := range []string{"SELECT 1", " with rows as (select 1) select * from rows"} {
		if err := validateReadOnly(sql); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
}

func TestValidateReadOnlyRejectsWrites(t *testing.T) {
	for _, sql := range []string{"DELETE FROM users", "SELECT 1; DROP TABLE users", "WITH x AS (UPDATE users SET a = 1) SELECT 1", "CALL mutate()"} {
		if err := validateReadOnly(sql); err == nil {
			t.Fatalf("accepted %q", sql)
		}
	}
}

func TestValidateReadOnlyAcceptsSelectAndWith(t *testing.T) {
	accepted := []string{
		"SELECT 1",
		"select 1",
		"  SELECT * FROM users",
		"\tWITH x AS (SELECT 1) SELECT * FROM x",
		"SELECT 1 -- trailing comment",
		"with x as (select 1) select * from x",
		"SELECT created, updated FROM t",
		"select 1; -- nothing after",
		"SELECT\n1",
		"SELECT\n-- comment\n1",
	}
	for _, sql := range accepted {
		if err := validateReadOnly(sql); err != nil {
			t.Errorf("accepted query %q rejected: %v", sql, err)
		}
	}
}

func TestValidateReadOnlyRejectsNonSelect(t *testing.T) {
	rejected := []string{
		"DELETE FROM users",
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET a = 1",
		"TRUNCATE t",
		"ALTER TABLE t ADD COLUMN x int",
		"CREATE TABLE t (id int)",
		"MERGE INTO t USING s ON true WHEN MATCHED THEN DO NOTHING",
		"GRANT ALL ON t TO u",
		"REVOKE ALL ON t FROM u",
		"CALL mutate()",
		"COPY t FROM stdin",
		"   INSERT INTO t VALUES (1)",
		"",
		"   ",
		"-- comment\nSELECT 1",
	}
	for _, sql := range rejected {
		if err := validateReadOnly(sql); err == nil {
			t.Errorf("rejected query %q accepted", sql)
		}
	}
}

func TestValidateReadOnlyReportsForbiddenKeyword(t *testing.T) {
	cases := []struct {
		sql string
		kw  string
	}{
		{"SELECT 1; DROP TABLE users", "DROP"},
		{"WITH x AS (UPDATE users SET a = 1) SELECT 1", "UPDATE"},
		{"SELECT * FROM t; DELETE FROM t", "DELETE"},
		{"SELECT 'DROP TABLE'", "DROP"},
		{"SELECT * FROM users WHERE name='delete'", "DELETE"},
		{"select 1; truncate t", "TRUNCATE"},
	}
	for _, c := range cases {
		err := validateReadOnly(c.sql)
		if err == nil {
			t.Errorf("query %q accepted", c.sql)
			continue
		}
		if !strings.Contains(err.Error(), c.kw) {
			t.Errorf("query %q error %q does not mention keyword %q", c.sql, err, c.kw)
		}
	}
}

func TestValidateReadOnlyRejectsLeadingComment(t *testing.T) {
	err := validateReadOnly("-- leading comment\nSELECT 1")
	if err == nil {
		t.Fatalf("query with leading comment accepted")
	}
	if !strings.Contains(err.Error(), "only SELECT and WITH") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateReadOnlyRejectsEmptyInput(t *testing.T) {
	for _, sql := range []string{"", "   "} {
		err := validateReadOnly(sql)
		if err == nil {
			t.Fatalf("empty input %q accepted", sql)
		}
		if !strings.Contains(err.Error(), "only SELECT and WITH") {
			t.Fatalf("empty input %q: unexpected error %v", sql, err)
		}
	}
}

const helperEnvKey = "GO_WANT_PGX_HELPER"
const helperArgsKey = "GO_PGX_TOOL_ARGS"
const helperArgSep = "\x1f"

func TestMainHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvKey) != "1" {
		return
	}
	os.Args = strings.Split(os.Getenv(helperArgsKey), helperArgSep)
	main()
}

func buildEnv(overrides map[string]string) []string {
	env := make([]string, 0)
	for _, kv := range os.Environ() {
		k := strings.SplitN(kv, "=", 2)[0]
		if _, ok := overrides[k]; ok {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

func runTool(t *testing.T, toolArgs []string, stdin string, overrides map[string]string) (string, string, int) {
	t.Helper()
	ov := map[string]string{
		helperEnvKey:  "1",
		helperArgsKey: strings.Join(toolArgs, helperArgSep),
	}
	for k, v := range overrides {
		ov[k] = v
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelperProcess$")
	cmd.Env = buildEnv(ov)
	cmd.Stdin = strings.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("failed to run helper: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

func TestMainDescribeProtocol(t *testing.T) {
	out, errOut, code := runTool(t, []string{"pgx", "describe"}, "", nil)
	if code != 0 {
		t.Fatalf("describe exit code %d, stderr %s", code, errOut)
	}
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	var spec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(line), &spec); err != nil {
		t.Fatalf("describe output %q is not JSON: %v", line, err)
	}
	if spec.Name != "postgres_query" {
		t.Fatalf("name = %q, want postgres_query", spec.Name)
	}
	if spec.Description == "" {
		t.Fatalf("empty description")
	}
	if _, ok := spec.Parameters.Properties["sql"]; !ok {
		t.Fatalf("missing sql parameter: %+v", spec.Parameters.Properties)
	}
	if len(spec.Parameters.Required) != 1 || spec.Parameters.Required[0] != "sql" {
		t.Fatalf("required = %v, want [sql]", spec.Parameters.Required)
	}
}

func TestMainDescribeAfterDashDash(t *testing.T) {
	out, errOut, code := runTool(t, []string{"pgx", "--", "describe"}, "", nil)
	if code != 0 {
		t.Fatalf("-- describe exit code %d, stderr %s", code, errOut)
	}
	if !strings.Contains(out, `"name":"postgres_query"`) {
		t.Fatalf("describe output missing protocol name: %q", out)
	}
}

func TestMainUsageNoArgs(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx"}, "", nil)
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "usage:") {
		t.Fatalf("stderr %q missing usage text", errOut)
	}
}

func TestMainUsageUnknownSubcommand(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "foo"}, "", nil)
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "usage:") {
		t.Fatalf("stderr %q missing usage text", errOut)
	}
}

func TestMainUsageRunWithoutTool(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run"}, "", nil)
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "usage:") {
		t.Fatalf("stderr %q missing usage text", errOut)
	}
}

func TestMainUsageWrongToolName(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "wrong_tool"}, "", nil)
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "usage:") {
		t.Fatalf("stderr %q missing usage text", errOut)
	}
}

func TestMainRunInvalidJSON(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "postgres_query"}, "{bad json", nil)
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "invalid arguments:") {
		t.Fatalf("stderr %q missing invalid arguments", errOut)
	}
}

func TestMainRunRejectsInvalidSQL(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "postgres_query"}, `{"sql":"DELETE FROM x"}`, nil)
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "only SELECT and WITH") {
		t.Fatalf("stderr %q missing validation error", errOut)
	}
}

func TestMainRunRejectsEmptySQL(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "postgres_query"}, `{"sql":""}`, nil)
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "only SELECT and WITH") {
		t.Fatalf("stderr %q missing validation error", errOut)
	}
}

func TestMainRunMissingDatabaseURL(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "postgres_query"}, `{"sql":"SELECT 1"}`, map[string]string{
		"DATABASE_URL":               "",
		"ALFRED_PICSEL_DATABASE_URL": "",
	})
	if code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "DATABASE_URL is required") {
		t.Fatalf("stderr %q missing DATABASE_URL error", errOut)
	}
}

func TestMainRunInvalidDatabaseURL(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "postgres_query"}, `{"sql":"SELECT 1"}`, map[string]string{
		"DATABASE_URL": "foobar",
	})
	if code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if !strings.Contains(errOut, "connect:") {
		t.Fatalf("stderr %q missing connect error", errOut)
	}
	if !strings.Contains(errOut, "cannot parse") {
		t.Fatalf("stderr %q missing parse error", errOut)
	}
}

func TestMainRunLegacyEnvFallback(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "postgres_query"}, `{"sql":"SELECT 1"}`, map[string]string{
		"DATABASE_URL":               "",
		"ALFRED_PICSEL_DATABASE_URL": "foobar_al",
	})
	if code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if !strings.Contains(errOut, "connect:") {
		t.Fatalf("stderr %q missing connect error", errOut)
	}
	if !strings.Contains(errOut, "foobar_al") {
		t.Fatalf("stderr %q missing legacy url in error", errOut)
	}
	if strings.Contains(errOut, "DATABASE_URL is required") {
		t.Fatalf("stderr %q wrongly reports missing URL", errOut)
	}
}

func TestMainRunPrefersDatabaseURL(t *testing.T) {
	_, errOut, code := runTool(t, []string{"pgx", "run", "postgres_query"}, `{"sql":"SELECT 1"}`, map[string]string{
		"DATABASE_URL":               "foobar_db",
		"ALFRED_PICSEL_DATABASE_URL": "foobar_al",
	})
	if code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if !strings.Contains(errOut, "foobar_db") {
		t.Fatalf("stderr %q missing preferred url", errOut)
	}
	if strings.Contains(errOut, "foobar_al") {
		t.Fatalf("stderr %q used legacy url over DATABASE_URL", errOut)
	}
}

func TestQueryLiveDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live DB tests in short mode")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = os.Getenv("ALFRED_PICSEL_DATABASE_URL")
	}
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()

	out, err := query(ctx, url, "SELECT 1 AS one")
	if err != nil {
		t.Fatalf("simple query: %v", err)
	}
	var parsed struct {
		Rows      []map[string]any `json:"rows"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("simple query output %q not JSON object: %v", out, err)
	}
	if parsed.Truncated || len(parsed.Rows) != 1 || parsed.Rows[0]["one"] != float64(1) {
		t.Fatalf("simple query output %q", out)
	}

	out, err = query(ctx, url, "SELECT 1 AS one WHERE false")
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("empty query output %q not JSON object: %v", out, err)
	}
	if parsed.Truncated || len(parsed.Rows) != 0 {
		t.Fatalf("empty result output %q", out)
	}

	out, err = query(ctx, url, "SELECT g AS n FROM generate_series(1, 2000) g")
	if err != nil {
		t.Fatalf("truncation query: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("truncated output %q not JSON object: %v", out, err)
	}
	if !parsed.Truncated {
		t.Fatalf("expected truncation flag: %q", out)
	}
	if len(parsed.Rows) != 1000 {
		t.Fatalf("truncated rows = %d, want 1000", len(parsed.Rows))
	}

	_, err = query(ctx, url, "CREATE TABLE t (id int)")
	if err == nil {
		t.Fatalf("read-only transaction accepted DDL")
	}
}
