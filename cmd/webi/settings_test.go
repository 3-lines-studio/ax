package main

import (
	"os"
	"strings"
	"testing"
)

type stateName struct {
	User   string `json:"user"`
	Config struct {
		Name string `json:"name"`
	} `json:"config"`
}

// The UI labels the person typing. The name lives in settings.json, reaches
// the browser as both state.user (the key the message list reads) and
// config.name (the settings form), and falls back to "You" in the UI when it
// is empty.
func TestSettingsCarryTheUserName(t *testing.T) {
	srv, _ := newTestServer(t, "/bin/true")

	var state stateName
	if code := call(t, srv, "GET", "/api/state", "", &state); code != 200 {
		t.Fatalf("state code = %d", code)
	}
	if state.User != "" || state.Config.Name != "" {
		t.Fatalf("fresh state has a name: %+v", state)
	}

	if code := call(t, srv, "POST", "/api/settings", `{"name":"  Don  "}`, nil); code != 200 {
		t.Fatalf("save name code = %d", code)
	}
	if code := call(t, srv, "GET", "/api/state", "", &state); code != 200 {
		t.Fatalf("state code = %d", code)
	}
	if state.User != "Don" || state.Config.Name != "Don" {
		t.Fatalf("state name = %q / %q, want Don", state.User, state.Config.Name)
	}

	data, err := os.ReadFile(srv.settings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"name": "Don"`) {
		t.Fatalf("settings.json does not keep the name:\n%s", data)
	}
}

func TestUserNameIsCapped(t *testing.T) {
	srv, _ := newTestServer(t, "/bin/true")
	long := strings.Repeat("a", 60)
	if code := call(t, srv, "POST", "/api/settings", `{"name":"`+long+`"}`, nil); code != 200 {
		t.Fatalf("save name code = %d", code)
	}
	var state stateName
	call(t, srv, "GET", "/api/state", "", &state)
	if len([]rune(state.User)) != 40 {
		t.Fatalf("name length = %d, want 40", len([]rune(state.User)))
	}
}
