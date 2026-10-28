package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	return NewVault(filepath.Join(t.TempDir(), "vault.json"))
}

func TestVaultLifecycle(t *testing.T) {
	vault := newTestVault(t)
	if vault.Exists() {
		t.Fatal("a fresh vault must not exist")
	}
	if err := vault.Unlock("whatever"); err != errVaultMissing {
		t.Fatalf("unlock without a vault: %v", err)
	}
	if err := vault.Create("short", "short"); err != errPINTooShort {
		t.Fatalf("short PIN: %v", err)
	}
	if err := vault.Create("long-enough", "different"); err != errPINMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	if !vault.Exists() || vault.Locked() {
		t.Fatalf("after create: exists=%v locked=%v", vault.Exists(), vault.Locked())
	}
	if err := vault.Create("long-enough", "long-enough"); err != errVaultExists {
		t.Fatalf("second create: %v", err)
	}

	vault.Lock()
	if !vault.Locked() {
		t.Fatal("lock did not lock")
	}
	if err := vault.Unlock("wrong-pin"); err != errWrongPIN {
		t.Fatalf("wrong PIN: %v", err)
	}
	if err := vault.Unlock("long-enough"); err != nil {
		t.Fatal(err)
	}
	if vault.Locked() {
		t.Fatal("unlock did not unlock")
	}
}

func TestVaultSecretsStayEncryptedOnDisk(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	secret := "sk-super-secret-value"
	if err := vault.Put("global", "DEEPSEEK_API_KEY", secret, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(vault.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte("DEEPSEEK")) {
		t.Fatalf("the vault file leaks plaintext: %s", data)
	}
	info, err := os.Stat(vault.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestVaultPutRenameAndDelete(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("global", "A", "1", ""); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("global", "B", "2", "A"); err != nil {
		t.Fatal(err)
	}
	names, _, err := vault.Names()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names["global"], ","); got != "B" {
		t.Fatalf("names = %q", got)
	}
	if err := vault.Delete("global", "B"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Delete("global", "B"); err != errNoSuchSecret {
		t.Fatalf("delete twice: %v", err)
	}
	if err := vault.Put("global", "bad name", "x", ""); err != errInvalidName {
		t.Fatalf("invalid name: %v", err)
	}
}

func TestVaultRevealNeedsThePIN(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("global", "TOKEN", "secret", ""); err != nil {
		t.Fatal(err)
	}
	value, err := vault.Reveal("global", "TOKEN", "long-enough")
	if err != nil || value != "secret" {
		t.Fatalf("reveal = %q err=%v", value, err)
	}
	if _, err := vault.Reveal("global", "TOKEN", "nope-nope"); err != errWrongPIN {
		t.Fatalf("wrong PIN: %v", err)
	}
	if err := vault.Check("long-enough"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Check("nope-nope"); err != errWrongPIN {
		t.Fatalf("check: %v", err)
	}
}

func TestVaultEnvMergesScopesAndFiles(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("global", "SHARED", "global-value", ""); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("team", "SHARED", "channel-value", ""); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("team", "ONLY_TEAM", "team-value", ""); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("team", "@file:KUBECONFIG", "apiVersion: v1\n", ""); err != nil {
		t.Fatal(err)
	}
	env, cleanup, err := vault.Env("team")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		got[name] = value
	}
	if got["SHARED"] != "channel-value" {
		t.Errorf("SHARED = %q; the channel scope must win", got["SHARED"])
	}
	if got["ONLY_TEAM"] != "team-value" {
		t.Errorf("ONLY_TEAM = %q", got["ONLY_TEAM"])
	}
	path := got["KUBECONFIG"]
	if path == "" {
		t.Fatal("KUBECONFIG was not materialized")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "apiVersion: v1\n" {
		t.Fatalf("file content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %o", info.Mode().Perm())
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup left the file: %v", err)
	}
}

func TestVaultEnvWhenLockedIsEmpty(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("global", "TOKEN", "secret", ""); err != nil {
		t.Fatal(err)
	}
	vault.Lock()
	env, cleanup, err := vault.Env("global")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if len(env) != 0 {
		t.Fatalf("env = %v", env)
	}
}

func TestVaultImportDotEnv(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	text := "# comment\nexport GH_TOKEN=abc\nQUOTED=\"with spaces\"\nSINGLE='x'\nBROKEN\nlower_name=ok\n"
	count, err := vault.Import("team", text)
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("imported = %d", count)
	}
	names, _, err := vault.Names()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names["team"], ","); got != "GH_TOKEN,QUOTED,SINGLE,lower_name" {
		t.Fatalf("names = %q", got)
	}
	value, err := vault.Reveal("team", "QUOTED", "long-enough")
	if err != nil || value != "with spaces" {
		t.Fatalf("QUOTED = %q err=%v", value, err)
	}
}
