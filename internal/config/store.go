package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// FileStore holds the active configuration in memory and mirrors it to a JSON
// file. Settings are a few dozen scalars, read on every request path, so a file
// is the right size of machinery; the document store is not involved.
type FileStore struct {
	mu      sync.RWMutex
	path    string
	current Config
	// dropped names keys the file on disk carried and this build no longer has.
	// Reported once at startup and cleared by the next save.
	dropped []string
}

// NewFileStore starts from base and overlays the file at path, if it exists.
// An empty path means memory only, which is what tests and --no-save want.
func NewFileStore(path string, base Config) (*FileStore, error) {
	if err := base.Validate(); err != nil {
		return nil, err
	}
	s := &FileStore{path: path, current: base}
	if path == "" {
		return s, nil
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}

	merged := base
	dropped, err := merged.ApplyStoredJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	s.current = merged
	s.dropped = dropped
	return s, nil
}

// Dropped lists the settings the file on disk held and this build retired, sorted.
// They are ignored rather than fatal (see ApplyStoredJSON); the next Save rewrites
// the file without them.
func (s *FileStore) Dropped() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.dropped)
}

func (s *FileStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Save validates and persists before updating memory, so a failed write leaves
// the previous values in force.
func (s *FileStore) Save(next Config) error {
	if err := next.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.write(next); err != nil {
		return err
	}
	// The rewrite carried only current keys, so whatever the older build left
	// behind is gone from disk now. Save already holds the lock.
	s.dropped = nil
	s.current = next
	return nil
}

// Apply overlays a partial JSON document on the current values and persists.
// It returns the resulting configuration.
func (s *FileStore) Apply(raw []byte) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.current
	if err := next.ApplyJSON(raw); err != nil {
		return s.current, err
	}
	if err := s.write(next); err != nil {
		return s.current, err
	}
	s.current = next
	return next, nil
}

func (s *FileStore) write(c Config) error {
	if s.path == "" {
		return nil
	}
	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	// 0600: this file can hold an API key.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
