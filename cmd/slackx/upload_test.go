package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"
)

func TestValidateFileReportsSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := validateFile(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got := info.Name(); got != "chart.png" {
		t.Fatalf("name=%q", got)
	}
	if got := info.Size(); got != 5 {
		t.Fatalf("size=%d", got)
	}
}

func TestValidateFileRejectsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.png")
	if _, err := validateFile(path); err == nil {
		t.Fatal("missing path accepted")
	}
}

func TestValidateFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := validateFile(dir)
	if err == nil {
		t.Fatal("directory accepted")
	}
	if !strings.Contains(err.Error(), "non-empty regular file") {
		t.Fatalf("err=%q", err)
	}
}

type capturedSlack struct {
	mu        sync.Mutex
	err       error
	getURL    url.Values
	completed url.Values
	fileName  string
	fileData  []byte
}

func (c *capturedSlack) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func newFakeSlack(t *testing.T) (*httptest.Server, *capturedSlack) {
	t.Helper()
	c := &capturedSlack{}
	mux := http.NewServeMux()
	mux.HandleFunc("/files.getUploadURLExternal", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			c.setErr(fmt.Errorf("parse getUpload form: %w", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		c.mu.Lock()
		c.getURL = r.PostForm
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"upload_url":"http://%s/upload-dest","file_id":"F123"}`, r.Host)
	})
	mux.HandleFunc("/upload-dest", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			c.setErr(fmt.Errorf("parse multipart: %w", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			c.setErr(fmt.Errorf("form file: %w", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		if err != nil {
			c.setErr(fmt.Errorf("read file: %w", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		c.mu.Lock()
		c.fileName = hdr.Filename
		c.fileData = data
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/files.completeUploadExternal", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			c.setErr(fmt.Errorf("parse complete form: %w", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		c.mu.Lock()
		c.completed = r.PostForm
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true,"files":[{"id":"F123"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, c
}

func writeTempFile(t *testing.T, name, content string) (string, os.FileInfo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, info
}

func TestUploadRequestBuildsExpectedPayload(t *testing.T) {
	srv, c := newFakeSlack(t)
	token := "xoxb-test-token"
	content := "chart bytes"
	path, info := writeTempFile(t, "chart.png", content)
	channel := "C123"
	thread := "1590000000.123456"
	client := slack.New(token, slack.OptionHTTPClient(srv.Client()), slack.OptionAPIURL(srv.URL+"/"))
	sum, err := client.UploadFileV2Context(context.Background(), slack.UploadFileV2Parameters{
		File:            path,
		FileSize:        int(info.Size()),
		Channel:         channel,
		ThreadTimestamp: thread,
		Filename:        filepath.Base(path),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if sum.ID != "F123" {
		t.Fatalf("id=%s", sum.ID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		t.Fatalf("server: %v", c.err)
	}
	if got := c.getURL.Get("token"); got != token {
		t.Fatalf("token=%q", got)
	}
	if got := c.getURL.Get("filename"); got != "chart.png" {
		t.Fatalf("filename=%q", got)
	}
	if got := c.getURL.Get("length"); got != strconv.Itoa(len(content)) {
		t.Fatalf("length=%q", got)
	}
	if c.fileName != "chart.png" {
		t.Fatalf("multipart filename=%q", c.fileName)
	}
	if string(c.fileData) != content {
		t.Fatalf("multipart data=%q", c.fileData)
	}
	if got := c.completed.Get("channel_id"); got != channel {
		t.Fatalf("channel_id=%q", got)
	}
	if got := c.completed.Get("thread_ts"); got != thread {
		t.Fatalf("thread_ts=%q", got)
	}
	if !strings.Contains(c.completed.Get("files"), "F123") {
		t.Fatalf("files=%q", c.completed.Get("files"))
	}
}

func TestUploadRequestOmitsThreadWhenEmpty(t *testing.T) {
	srv, c := newFakeSlack(t)
	token := "xoxb-test-token"
	path, info := writeTempFile(t, "chart.png", "chart bytes")
	client := slack.New(token, slack.OptionHTTPClient(srv.Client()), slack.OptionAPIURL(srv.URL+"/"))
	sum, err := client.UploadFileV2Context(context.Background(), slack.UploadFileV2Parameters{
		File:     path,
		FileSize: int(info.Size()),
		Channel:  "C123",
		Filename: filepath.Base(path),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if sum.ID != "F123" {
		t.Fatalf("id=%s", sum.ID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		t.Fatalf("server: %v", c.err)
	}
	if got := c.completed.Get("thread_ts"); got != "" {
		t.Fatalf("thread_ts=%q, want empty", got)
	}
	if got := c.completed.Get("channel_id"); got != "C123" {
		t.Fatalf("channel_id=%q", got)
	}
	if got := c.completed.Get("files"); !strings.Contains(got, "F123") {
		t.Fatalf("files=%q", got)
	}
}
