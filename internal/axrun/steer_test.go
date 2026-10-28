package axrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSteerSendsTheText(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "steer.txt")
	script := "#!/bin/sh\n" +
		"read line\n" +
		"printf '%s' \"$line\" > " + record + "\n" +
		"cat <<'EOF'\n" +
		"{\"type\":\"protocol\",\"version\":1}\n" +
		"{\"type\":\"done\",\"outcome\":\"done\"}\n" +
		"EOF\n"
	path := filepath.Join(dir, "ax")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := Start(context.Background(), Config{AXPath: path}, Job{Prompt: "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := run.Steer("also update the changelog"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("steer never delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := run.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var control struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatalf("steer line is not JSON: %s", data)
	}
	if control.Type != "steer" || control.Text != "also update the changelog" {
		t.Fatalf("steer line = %s", data)
	}
	if err := run.Steer("late"); err == nil {
		t.Fatal("steering a finished run must fail")
	}
}
