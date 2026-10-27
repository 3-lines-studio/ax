package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLoadImagesStoresBlobAndBuildsDataURL(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "run.jsonl")
	path := filepath.Join(dir, "shot.png")
	data := testPNG(t)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := loadImages([]string{path}, "low", session)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Blob == "" || refs[0].MediaType != "image/png" || refs[0].Detail != "low" {
		t.Fatalf("refs = %+v", refs)
	}
	if refs[0].URL != "" {
		t.Errorf("durable image should not inline data: %q", refs[0].URL)
	}
	url, err := imageURL(refs[0], session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("url = %q", url[:40])
	}
}

func TestLoadImagesWithoutSessionInlines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := loadImages([]string{path}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || !strings.HasPrefix(refs[0].URL, "data:image/png;base64,") || refs[0].Blob != "" {
		t.Errorf("refs = %+v", refs)
	}
}

func TestLoadImagesRejectsUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-image.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadImages([]string{path}, "auto", ""); err == nil {
		t.Fatal("expected unsupported format error")
	}
}

func TestLoadImagesURL(t *testing.T) {
	refs, err := loadImages([]string{"https://example.com/image.jpg"}, "original", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].URL != "https://example.com/image.jpg" || refs[0].Detail != "original" {
		t.Errorf("refs = %+v", refs)
	}
}

func TestImageBlobVerification(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "run.jsonl")
	refs, err := loadImages([]string{writeTempPNG(t, dir)}, "auto", session)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blobDir(session), refs[0].Blob)
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := imageURL(refs[0], session); err == nil {
		t.Fatal("expected checksum failure")
	}
}

func writeTempPNG(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVisionRequestParts(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "run.jsonl")
	refs, err := loadImages([]string{writeTempPNG(t, dir)}, "low", session)
	if err != nil {
		t.Fatal(err)
	}
	message := buildUserMessage("describe this", refs)
	if !message.HasImages() || message.Content != "describe this" || len(message.ContentParts) != 2 {
		t.Fatalf("message = %+v", message)
	}
	o := NewOpenAI("https://api.deepseek.com", "k")
	o.SessionPath = session
	built, err := o.buildRequest(&Request{Model: "deepseek-flash", Messages: []Message{message}}, true)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeRequestBody(t, built.body)
	user := body["messages"].([]any)[0].(map[string]any)
	parts := user["content"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["type"] != "text" {
		t.Fatalf("parts = %v", parts)
	}
	image := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Errorf("image part = %v", image)
	}
	ref := image["image_url"].(map[string]any)
	if ref["detail"] != "low" || !strings.HasPrefix(ref["url"].(string), "data:image/png;base64,") {
		t.Errorf("image_url = %v", ref)
	}
}

func TestVisionRequestPartsRequireBlobSession(t *testing.T) {
	ref := ImageRef{Blob: strings.Repeat("a", 64), MediaType: "image/png"}
	message := buildUserMessage("x", []ImageRef{ref})
	o := NewOpenAI("https://api.deepseek.com", "k")
	_, err := o.buildRequest(&Request{Model: "deepseek-flash", Messages: []Message{message}}, true)
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Errorf("err = %v", err)
	}
}

func TestMessagePartsSurviveJSON(t *testing.T) {
	message := buildUserMessage("x", []ImageRef{{Blob: strings.Repeat("b", 64), MediaType: "image/png", Detail: "auto"}})
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.HasImages() || decoded.ContentParts[1].Image == nil || decoded.ContentParts[1].Image.Blob == "" {
		t.Errorf("decoded = %+v", decoded)
	}
}
