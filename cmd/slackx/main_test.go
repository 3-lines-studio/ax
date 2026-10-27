package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFile(path); err != nil {
		t.Fatal(err)
	}
}

func TestValidateFileRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFile(path); err == nil {
		t.Fatal("empty file was accepted")
	}
}
