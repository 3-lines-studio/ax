package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxImageBytes = 32 << 20
	maxImageURL   = 8192
)

func buildUserMessage(prompt string, images []ImageRef) Message {
	message := Message{Role: "user", Content: prompt}
	if len(images) == 0 {
		return message
	}
	parts := make([]ContentPart, 0, len(images)+1)
	if prompt != "" {
		parts = append(parts, ContentPart{Type: "text", Text: prompt})
	}
	for i := range images {
		image := images[i]
		parts = append(parts, ContentPart{Type: "image", Image: &image})
	}
	message.ContentParts = parts
	return message
}

func loadImages(specs []string, detail, sessionPath string) ([]ImageRef, error) {
	if detail == "" {
		detail = "auto"
	}
	switch detail {
	case "low", "high", "original", "auto":
	default:
		return nil, fmt.Errorf("invalid image detail %q (use low, high, original or auto)", detail)
	}
	var refs []ImageRef
	for _, spec := range specs {
		if spec == "" {
			return nil, fmt.Errorf("empty image path or URL")
		}
		if strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://") {
			if len(spec) > maxImageURL {
				return nil, fmt.Errorf("image URL is longer than %d characters", maxImageURL)
			}
			if _, err := url.ParseRequestURI(spec); err != nil {
				return nil, fmt.Errorf("invalid image URL: %w", err)
			}
			refs = append(refs, ImageRef{URL: spec, Detail: detail})
			continue
		}
		data, mediaType, err := readImageFile(spec)
		if err != nil {
			return nil, err
		}
		ref := ImageRef{MediaType: mediaType, Detail: detail, Name: filepath.Base(spec)}
		if sessionPath == "" {
			ref.URL = dataURL(mediaType, data)
		} else {
			blob, err := storeBlob(sessionPath, data)
			if err != nil {
				return nil, err
			}
			ref.Blob = blob
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func readImageFile(path string) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("read image %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image %s: %w", path, err)
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("image %s exceeds the %d MiB inline limit; use an http(s) URL", path, maxImageBytes>>20)
	}
	mediaType := detectImageType(data)
	if mediaType == "" {
		return nil, "", fmt.Errorf("image %s is not JPEG, PNG, GIF or WebP", path)
	}
	return data, mediaType, nil
}

func detectImageType(data []byte) string {
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	switch http.DetectContentType(data) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return http.DetectContentType(data)
	default:
		return ""
	}
}

func dataURL(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func blobDir(sessionPath string) string {
	return sessionPath + ".blobs"
}

func storeBlob(sessionPath string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])
	dir := blobDir(sessionPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create image store: %w", err)
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, nil
	}
	tmp, err := os.CreateTemp(dir, ".ax-image-*")
	if err != nil {
		return "", fmt.Errorf("create image blob: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("write image blob: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sync image blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("close image blob: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("protect image blob: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("publish image blob: %w", err)
	}
	syncDir(dir)
	return name, nil
}

func readBlob(sessionPath, name string) ([]byte, error) {
	if len(name) != 64 {
		return nil, fmt.Errorf("invalid image blob id")
	}
	if _, err := hex.DecodeString(name); err != nil {
		return nil, fmt.Errorf("invalid image blob id")
	}
	data, err := os.ReadFile(filepath.Join(blobDir(sessionPath), name))
	if err != nil {
		return nil, fmt.Errorf("read image blob %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != name {
		return nil, fmt.Errorf("image blob %s is corrupt", name)
	}
	return data, nil
}

func imageURL(ref ImageRef, sessionPath string) (string, error) {
	if ref.URL != "" {
		return ref.URL, nil
	}
	if ref.Blob == "" {
		return "", fmt.Errorf("image reference has neither URL nor blob")
	}
	if sessionPath == "" {
		return "", fmt.Errorf("image blob requires a session file")
	}
	data, err := readBlob(sessionPath, ref.Blob)
	if err != nil {
		return "", err
	}
	mediaType := ref.MediaType
	if mediaType == "" {
		mediaType = detectImageType(data)
	}
	if mediaType == "" {
		return "", fmt.Errorf("image blob %s is not a supported image", ref.Blob)
	}
	return dataURL(mediaType, data), nil
}
