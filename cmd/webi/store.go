package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store keeps webi's own metadata: channels (bot directories) and threads
// (an ax session plus a title). The conversation itself lives in the ax
// session file, which is the same format slaxi uses.
type Store struct {
	dir string
	mu  sync.Mutex
}

type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Workdir string `json:"workdir"`
	Prompt  string `json:"prompt,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
	// VaultScope picks which secrets reach this channel's runs. Empty means
	// the shared `global` scope.
	VaultScope string    `json:"vault_scope,omitempty"`
	Created    time.Time `json:"created"`
}

type Thread struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Title   string `json:"title"`
	// TitleGenerated marks a model-written title, so a thread keeps the name it
	// earned instead of being renamed after every run.
	TitleGenerated bool      `json:"title_generated,omitempty"`
	Created        time.Time `json:"created"`
	Updated        time.Time `json:"updated"`
	Preview        string    `json:"preview,omitempty"`
	Running        bool      `json:"running,omitempty"`
}

type channelsFile struct {
	Channels []Channel `json:"channels"`
}

func NewStore(dir string) (*Store, error) {
	for _, sub := range []string{"", "threads", "blobs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	st := &Store{dir: dir}
	if len(st.Channels()) == 0 {
		if err := st.PutChannel(Channel{ID: "main", Name: "main", Workdir: st.dir, Created: time.Now().UTC()}); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func (s *Store) channelsPath() string { return filepath.Join(s.dir, "channels.json") }

func (s *Store) Channels() []Channel {
	data, err := os.ReadFile(s.channelsPath())
	if err != nil {
		return nil
	}
	var file channelsFile
	if json.Unmarshal(data, &file) != nil {
		return nil
	}
	sort.SliceStable(file.Channels, func(i, j int) bool { return file.Channels[i].Created.Before(file.Channels[j].Created) })
	return file.Channels
}

func (s *Store) Channel(id string) (Channel, bool) {
	for _, channel := range s.Channels() {
		if channel.ID == id {
			return channel, true
		}
	}
	return Channel{}, false
}

func (s *Store) PutChannel(channel Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	channels := s.Channels()
	replaced := false
	for i := range channels {
		if channels[i].ID == channel.ID {
			channels[i], replaced = channel, true
		}
	}
	if !replaced {
		channels = append(channels, channel)
	}
	data, err := json.MarshalIndent(channelsFile{Channels: channels}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.channelsPath(), data, 0o600)
}

func (s *Store) DeleteChannel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	channels := s.Channels()
	out := channels[:0]
	for _, channel := range channels {
		if channel.ID != id {
			out = append(out, channel)
		}
	}
	data, err := json.MarshalIndent(channelsFile{Channels: out}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.channelsPath(), data, 0o600)
}

func (s *Store) threadDir(id string) string { return filepath.Join(s.dir, "threads", id) }

// SessionPath is the ax log of one thread.
func (s *Store) SessionPath(id string) string { return filepath.Join(s.threadDir(id), "session.jsonl") }

func (s *Store) CreateThread(channel string, title string) (Thread, error) {
	if _, ok := s.Channel(channel); !ok {
		return Thread{}, errors.New("unknown channel")
	}
	if strings.TrimSpace(title) == "" {
		title = "New chat"
	}
	id, err := randomID()
	if err != nil {
		return Thread{}, err
	}
	now := time.Now().UTC()
	thread := Thread{ID: id, Channel: channel, Title: title, Created: now, Updated: now}
	if err := os.MkdirAll(s.threadDir(id), 0o700); err != nil {
		return Thread{}, err
	}
	return thread, s.PutThread(thread)
}

func (s *Store) PutThread(thread Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(thread, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.threadDir(thread.ID), "meta.json"), data, 0o600)
}

func (s *Store) Thread(id string) (Thread, error) {
	data, err := os.ReadFile(filepath.Join(s.threadDir(id), "meta.json"))
	if err != nil {
		return Thread{}, errors.New("thread not found")
	}
	var thread Thread
	if err := json.Unmarshal(data, &thread); err != nil {
		return Thread{}, err
	}
	return thread, nil
}

func (s *Store) Threads(channel string) []Thread {
	entries, err := os.ReadDir(filepath.Join(s.dir, "threads"))
	if err != nil {
		return nil
	}
	out := make([]Thread, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		thread, err := s.Thread(entry.Name())
		if err != nil {
			continue
		}
		if channel != "" && thread.Channel != channel {
			continue
		}
		out = append(out, thread)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

func (s *Store) DeleteThread(id string) error {
	thread, err := s.Thread(id)
	if err != nil {
		return err
	}
	_ = s.Blobs(thread) // keeps blobs alive until the thread is gone
	return os.RemoveAll(s.threadDir(id))
}

// Touch records activity and derives a title from the first user message.
func (s *Store) Touch(id string, text string) {
	thread, err := s.Thread(id)
	if err != nil {
		return
	}
	thread.Updated = time.Now().UTC()
	thread.Preview = truncate(oneLine(text), 120)
	if thread.Title == "New chat" && strings.TrimSpace(text) != "" {
		thread.Title = truncate(oneLine(text), 60)
	}
	_ = s.PutThread(thread)
}

// SetTitle stores a generated title once. A later run does not rename a thread
// that already has one.
func (s *Store) SetTitle(id, title string) error {
	thread, err := s.Thread(id)
	if err != nil {
		return err
	}
	if thread.TitleGenerated {
		return nil
	}
	thread.Title = title
	thread.TitleGenerated = true
	thread.Updated = time.Now().UTC()
	return s.PutThread(thread)
}

// Blobs returns the image files of a thread, the ones ax resolves with
// -image. webi keeps them next to the session so a thread is self-contained.
func (s *Store) Blobs(thread Thread) string {
	dir := filepath.Join(s.threadDir(thread.ID), "images")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func (s *Store) SaveBlob(data []byte, ext string) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", err
	}
	if ext == "" {
		ext = ".bin"
	}
	path := filepath.Join(s.dir, "blobs", id+ext)
	return id, writeFileAtomic(path, data, 0o600)
}

func (s *Store) BlobPath(id string) (string, bool) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "blobs"))
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if name := entry.Name(); strings.TrimSuffix(name, filepath.Ext(name)) == id {
			return filepath.Join(s.dir, "blobs", name), true
		}
	}
	return "", false
}

func randomID() (string, error) {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func truncate(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
