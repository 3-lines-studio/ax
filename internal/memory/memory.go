// Package memory stores small personal facts under the bot's runtime state.
//
// The store is deliberately file-based: one JSON file per memory under
// state/memory/. It is readable and exportable with ordinary tools, and the
// memoryx provider exposes it to ax through the tool catalog.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ErrNotFound is returned when a memory id does not exist or was deleted.
var ErrNotFound = errors.New("memory not found")

// Memory is one durable personal fact. Subject and Summary are the searchable
// surface; Body holds details. SourceURIs point back to the original material.
type Memory struct {
	ID         string     `json:"id"`
	Subject    string     `json:"subject"`
	Summary    string     `json:"summary"`
	Body       string     `json:"body,omitempty"`
	Category   string     `json:"category,omitempty"`
	SourceURIs []string   `json:"source_uris,omitempty"`
	DedupeKey  string     `json:"dedupe_key,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

// Store is a directory of memory JSON files.
type Store struct {
	Root string
}

// New returns a store rooted at dir. The directory is created on the first
// write, so reads from a missing store simply return nothing.
func New(dir string) *Store {
	return &Store{Root: dir}
}

// List returns active memories, newest first. limit <= 0 means no limit.
func (s *Store) List(limit int) ([]Memory, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	items := make([]Memory, 0, len(all))
	for _, item := range all {
		if item.DeletedAt == nil {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return trim(items, limit), nil
}

// Search ranks active memories by a simple lexical score. An empty query
// returns the newest memories. limit <= 0 uses 5, capped at 20.
func (s *Store) Search(query string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return s.List(limit)
	}
	tokens := tokens(query)
	matches := make([]scored, 0)
	for _, item := range all {
		if item.DeletedAt != nil {
			continue
		}
		if score := score(item, query, tokens); score > 0 {
			matches = append(matches, scored{item: item, score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].item.UpdatedAt.After(matches[j].item.UpdatedAt)
	})
	items := make([]Memory, 0, len(matches))
	for _, match := range matches {
		items = append(items, match.item)
	}
	return trim(items, limit), nil
}

// Get returns an active memory by id.
func (s *Store) Get(id string) (Memory, error) {
	item, err := s.get(id)
	if err != nil {
		return Memory{}, err
	}
	if item.DeletedAt != nil {
		return Memory{}, ErrNotFound
	}
	return item, nil
}

// Upsert creates a memory, updates one with the same dedupe key, or updates
// the memory named by in.ID. It returns the stored value and whether it was
// newly created. A deleted memory with the same dedupe key is restored.
func (s *Store) Upsert(in Memory) (Memory, bool, error) {
	if err := normalize(&in); err != nil {
		return Memory{}, false, err
	}
	now := time.Now().UTC()
	var existing *Memory
	if in.ID != "" {
		item, err := s.get(in.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Memory{}, false, err
		}
		if err == nil {
			existing = &item
		}
	} else if in.DedupeKey != "" {
		all, err := s.readAll()
		if err != nil {
			return Memory{}, false, err
		}
		for i := range all {
			if all[i].DedupeKey == in.DedupeKey {
				existing = &all[i]
				break
			}
		}
	}
	created := existing == nil
	if created {
		if in.ID == "" {
			id, err := s.newID()
			if err != nil {
				return Memory{}, false, err
			}
			in.ID = id
		}
		in.CreatedAt = now
		in.DeletedAt = nil
	} else {
		in.ID = existing.ID
		in.CreatedAt = existing.CreatedAt
		in.DeletedAt = nil
	}
	in.UpdatedAt = now
	if err := s.write(in); err != nil {
		return Memory{}, false, err
	}
	return in, created, nil
}

// Delete soft-deletes a memory. force removes the file. A missing memory is
// ErrNotFound.
func (s *Store) Delete(id string, force bool) error {
	item, err := s.get(id)
	if err != nil {
		return err
	}
	path := s.path(item.ID)
	if force {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				return ErrNotFound
			}
			return err
		}
		return nil
	}
	if item.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	item.DeletedAt = &now
	item.UpdatedAt = now
	return s.write(item)
}

type scored struct {
	item  Memory
	score int
}

func score(item Memory, query string, tokens []string) int {
	subject := strings.ToLower(item.Subject)
	summary := strings.ToLower(item.Summary)
	body := strings.ToLower(item.Body)
	category := strings.ToLower(item.Category)
	total := 0
	if strings.Contains(subject, query) {
		total += 4
	}
	if strings.Contains(summary, query) {
		total += 3
	}
	if strings.Contains(body, query) {
		total += 2
	}
	for _, token := range tokens {
		if strings.Contains(subject, token) {
			total += 3
		}
		if strings.Contains(summary, token) {
			total += 2
		}
		if strings.Contains(body, token) {
			total++
		}
		if strings.Contains(category, token) || strings.Contains(item.DedupeKey, token) {
			total++
		}
	}
	return total
}

func tokens(value string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		field = strings.ToLower(field)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func normalize(item *Memory) error {
	item.Subject = strings.TrimSpace(item.Subject)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Body = strings.TrimSpace(item.Body)
	item.Category = strings.TrimSpace(item.Category)
	item.DedupeKey = strings.TrimSpace(item.DedupeKey)
	if item.Subject == "" {
		return errors.New("subject is required")
	}
	if item.Summary == "" {
		return errors.New("summary is required")
	}
	if len(item.Subject) > 200 {
		return errors.New("subject is longer than 200 bytes")
	}
	if len(item.Summary) > 2000 {
		return errors.New("summary is longer than 2000 bytes")
	}
	if len(item.Body) > 32<<10 {
		return errors.New("body is longer than 32 KiB")
	}
	if len(item.Category) > 64 {
		return errors.New("category is longer than 64 bytes")
	}
	if len(item.DedupeKey) > 128 {
		return errors.New("dedupe_key is longer than 128 bytes")
	}
	uris := item.SourceURIs[:0]
	seen := make(map[string]bool)
	for _, uri := range item.SourceURIs {
		uri = strings.TrimSpace(uri)
		if uri == "" || seen[uri] {
			continue
		}
		if len(uri) > 2048 {
			return errors.New("source_uri is longer than 2048 bytes")
		}
		seen[uri] = true
		uris = append(uris, uri)
	}
	if len(uris) > 10 {
		return errors.New("more than 10 source_uris")
	}
	item.SourceURIs = uris
	return nil
}

func (s *Store) readAll() ([]Memory, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory directory: %w", err)
	}
	items := make([]Memory, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validID(id) {
			continue
		}
		item, err := s.get(id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) get(id string) (Memory, error) {
	if !validID(id) {
		return Memory{}, ErrNotFound
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Memory{}, ErrNotFound
		}
		return Memory{}, err
	}
	var item Memory
	if err := json.Unmarshal(data, &item); err != nil {
		return Memory{}, fmt.Errorf("parse memory %s: %w", id, err)
	}
	if item.ID == "" {
		item.ID = id
	}
	if !validID(item.ID) || item.ID != id {
		return Memory{}, fmt.Errorf("memory %s has invalid id %q", id, item.ID)
	}
	return item, nil
}

func (s *Store) write(item Memory) error {
	if !validID(item.ID) {
		return errors.New("invalid memory id")
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.Root, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path(item.ID)); err != nil {
		return err
	}
	if dir, err := os.Open(s.Root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.Root, id+".json")
}

func (s *Store) newID() (string, error) {
	for i := 0; i < 5; i++ {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		id := time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(buf)
		if _, err := os.Stat(s.path(id)); os.IsNotExist(err) {
			return id, nil
		}
	}
	return "", errors.New("could not allocate a memory id")
}

func validID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func trim(items []Memory, limit int) []Memory {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}
