package main

import (
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/3-lines-studio/ax/internal/axrun"
)

func TestVaultAdopt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.json")
	vault := NewVault(path)
	if err := vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put("global", "DEEPSEEK_API_KEY", "sk-test", ""); err != nil {
		t.Fatal(err)
	}
	key := vault.Key()
	if len(key) != 32 {
		t.Fatalf("key length = %d", len(key))
	}

	locked := NewVault(path)
	if locked.Has("global", "DEEPSEEK_API_KEY") {
		t.Fatal("a fresh vault must be locked")
	}
	if err := locked.Adopt(key); err != nil {
		t.Fatal(err)
	}
	if !locked.Has("global", "DEEPSEEK_API_KEY") {
		t.Fatal("adopt did not unlock")
	}

	stale := NewVault(path)
	if err := stale.Adopt(make([]byte, 32)); err == nil {
		t.Fatal("a wrong key must be rejected")
	}
	if stale.Has("global", "DEEPSEEK_API_KEY") {
		t.Fatal("a rejected key must leave the vault locked")
	}
}

func TestShareVaultKey(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var method, key string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method = r.Method
		if r.Method == http.MethodPut {
			var req struct {
				Key string `json:"key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			key = req.Key
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	t.Setenv("AX_HOST_SOCK", sock)
	state := t.TempDir()
	store, err := NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, axrun.Config{AXPath: "/bin/true", BotRoot: t.TempDir()}, state, nil, "test")
	if err := srv.vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	srv.shareVaultKey(false)
	want := hex.EncodeToString(srv.vault.Key())
	mu.Lock()
	if method != http.MethodPut || key != want {
		t.Fatalf("shared %s %q, want PUT %q", method, key, want)
	}
	mu.Unlock()

	srv.vault.Lock()
	srv.shareVaultKey(true)
	mu.Lock()
	if method != http.MethodDelete {
		t.Fatalf("forget method = %s", method)
	}
	mu.Unlock()
}

func TestAdoptVaultKeyFromFD(t *testing.T) {
	state := t.TempDir()
	store, err := NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, axrun.Config{AXPath: "/bin/true", BotRoot: t.TempDir()}, state, nil, "test")
	if err := srv.vault.Create("long-enough", "long-enough"); err != nil {
		t.Fatal(err)
	}
	if err := srv.vault.Put("global", "DEEPSEEK_API_KEY", "sk-test", ""); err != nil {
		t.Fatal(err)
	}
	key := srv.vault.Key()

	other, err := NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewServer(other, axrun.Config{AXPath: "/bin/true", BotRoot: t.TempDir()}, state, nil, "test")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AX_VAULT_FD", strconv.Itoa(int(read.Fd())))
	go func() {
		_, _ = write.Write(key)
		_ = write.Close()
	}()
	restarted.adoptVaultKey()
	if !restarted.vault.Has("global", "DEEPSEEK_API_KEY") {
		t.Fatal("the key from the supervisor was not adopted")
	}

	short, swrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AX_VAULT_FD", strconv.Itoa(int(short.Fd())))
	go func() {
		_, _ = swrite.Write([]byte("short"))
		_ = swrite.Close()
	}()
	broken := NewServer(other, axrun.Config{AXPath: "/bin/true", BotRoot: t.TempDir()}, state, nil, "test")
	broken.adoptVaultKey()
	if broken.vault.Has("global", "DEEPSEEK_API_KEY") {
		t.Fatal("a short key must not unlock the vault")
	}
}
