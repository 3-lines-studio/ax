package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVaultAPILifecycle(t *testing.T) {
	fake, _ := fakeAX(t)
	srv, _ := newTestServer(t, fake)

	var state struct {
		Vault map[string]any `json:"vault"`
	}
	call(t, srv, "GET", "/api/state", "", &state)
	if state.Vault["exists"] != false || state.Vault["locked"] != false {
		t.Fatalf("vault = %+v", state.Vault)
	}
	if code := call(t, srv, "POST", "/api/vault/init", `{"pin":"1234","confirm":"1234"}`, nil); code != 400 {
		t.Fatalf("short PIN code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/vault/init", `{"pin":"long-enough","confirm":"long-enough"}`, nil); code != 200 {
		t.Fatalf("init code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/vault/item", `{"scope":"global","name":"TOKEN","value":"s3cret"}`, nil); code != 200 {
		t.Fatalf("item code=%d", code)
	}

	var list struct {
		Exists   bool                `json:"exists"`
		Locked   bool                `json:"locked"`
		Vars     map[string][]string `json:"vars"`
		LastUsed map[string]int64    `json:"last_used"`
	}
	if code := call(t, srv, "GET", "/api/vault", "", &list); code != 200 {
		t.Fatalf("state code=%d", code)
	}
	if !list.Exists || list.Locked || strings.Join(list.Vars["global"], ",") != "TOKEN" {
		t.Fatalf("list = %+v", list)
	}

	var revealed struct {
		Value string `json:"value"`
	}
	if code := call(t, srv, "POST", "/api/vault/reveal", `{"scope":"global","name":"TOKEN","pin":"long-enough"}`, &revealed); code != 200 || revealed.Value != "s3cret" {
		t.Fatalf("reveal code=%d %+v", code, revealed)
	}
	if code := call(t, srv, "POST", "/api/vault/reveal", `{"scope":"global","name":"TOKEN","pin":"wrong-pin"}`, nil); code != 400 {
		t.Fatalf("wrong PIN code=%d", code)
	}

	if code := call(t, srv, "POST", "/api/vault/lock", "", nil); code != 200 {
		t.Fatalf("lock code=%d", code)
	}
	var lockedList struct {
		Exists bool                `json:"exists"`
		Locked bool                `json:"locked"`
		Vars   map[string][]string `json:"vars"`
	}
	call(t, srv, "GET", "/api/vault", "", &lockedList)
	if !lockedList.Exists || !lockedList.Locked || len(lockedList.Vars) != 0 {
		t.Fatalf("locked list = %+v", lockedList)
	}
	if code := call(t, srv, "POST", "/api/vault/unlock", `{"pin":"wrong-pin"}`, nil); code != 400 {
		t.Fatalf("unlock wrong code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/vault/unlock", `{"pin":"long-enough"}`, nil); code != 200 {
		t.Fatalf("unlock code=%d", code)
	}
}

func TestVaultKeyCountsAsAKey(t *testing.T) {
	fake, _ := fakeAX(t)
	srv, _ := newTestServer(t, fake)
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if srv.hasKey() {
		t.Fatal("no key anywhere")
	}
	if code := call(t, srv, "POST", "/api/vault/init", `{"pin":"long-enough","confirm":"long-enough"}`, nil); code != 200 {
		t.Fatalf("init code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/vault/item", `{"scope":"global","name":"DEEPSEEK_API_KEY","value":"sk-vault"}`, nil); code != 200 {
		t.Fatalf("item code=%d", code)
	}
	if !srv.hasKey() {
		t.Fatal("a key inside the vault must count")
	}
}

func TestRunReceivesVaultSecrets(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$GLOBAL_TOKEN\" \"$TEAM_TOKEN\" \"$KUBECONFIG\" > " + record + "\n" +
		"if [ -n \"$KUBECONFIG\" ]; then cat \"$KUBECONFIG\" >> " + record + "; fi\n" +
		"printf '%s\\n' '{\"type\":\"done\",\"outcome\":\"done\"}'\n"
	axPath := filepath.Join(dir, "ax")
	if err := os.WriteFile(axPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, channel := newTestServer(t, axPath)
	srv.cfg.Env = []string{"PATH=" + os.Getenv("PATH")}

	if code := call(t, srv, "POST", "/api/vault/init", `{"pin":"long-enough","confirm":"long-enough"}`, nil); code != 200 {
		t.Fatalf("init code=%d", code)
	}
	channels := srv.store.Channels()
	for i := range channels {
		if channels[i].ID == channel {
			channels[i].VaultScope = "team"
			if err := srv.store.PutChannel(channels[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, item := range []string{
		`{"scope":"global","name":"GLOBAL_TOKEN","value":"g"}`,
		`{"scope":"team","name":"TEAM_TOKEN","value":"t"}`,
		`{"scope":"team","name":"@file:KUBECONFIG","value":"kube-data"}`,
	} {
		if code := call(t, srv, "POST", "/api/vault/item", item, nil); code != 200 {
			t.Fatalf("item %s code=%d", item, code)
		}
	}

	var thread Thread
	if code := call(t, srv, "POST", "/api/channels/"+channel+"/threads", `{"title":""}`, &thread); code != 200 {
		t.Fatalf("create thread code=%d", code)
	}
	if code := call(t, srv, "POST", "/api/threads/"+thread.ID+"/messages", `{"text":"hi"}`, nil); code != 200 {
		t.Fatalf("message code=%d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for srv.running(thread.ID) {
		if time.Now().After(deadline) {
			t.Fatal("the run did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the run never wrote its environment: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if lines[0] != "g" || lines[1] != "t" {
		t.Fatalf("env = %q", lines)
	}
	if len(lines) < 3 || !strings.HasPrefix(lines[2], "/") {
		t.Fatalf("KUBECONFIG = %q", lines)
	}
	if lines[len(lines)-1] != "kube-data" {
		t.Fatalf("file content = %q", lines)
	}
	if _, err := os.Stat(lines[2]); !os.IsNotExist(err) {
		t.Fatalf("secret file outlived the run: %v", err)
	}
	var vaultState struct {
		LastUsed map[string]int64 `json:"last_used"`
	}
	call(t, srv, "GET", "/api/vault", "", &vaultState)
	if vaultState.LastUsed["team/TEAM_TOKEN"] == 0 {
		t.Fatalf("last_used = %+v", vaultState.LastUsed)
	}
}

func TestVaultImportEndpoint(t *testing.T) {
	fake, _ := fakeAX(t)
	srv, _ := newTestServer(t, fake)
	if code := call(t, srv, "POST", "/api/vault/init", `{"pin":"long-enough","confirm":"long-enough"}`, nil); code != 200 {
		t.Fatalf("init code=%d", code)
	}
	var imported struct {
		Imported int `json:"imported"`
	}
	if code := call(t, srv, "POST", "/api/vault/import", `{"scope":"global","text":"A=1\nB=\"two\"\n# x\n"}`, &imported); code != 200 || imported.Imported != 2 {
		t.Fatalf("import code=%d %+v", code, imported)
	}
	var listed struct {
		Vars map[string][]string `json:"vars"`
	}
	call(t, srv, "GET", "/api/vault", "", &listed)
	if strings.Join(listed.Vars["global"], ",") != "A,B" {
		t.Fatalf("vars = %+v", listed.Vars)
	}
}

var _ = json.Marshal
