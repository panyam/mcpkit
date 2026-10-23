package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store persists the bridge's watches. Save receives every watch, sorted by
// id, each time one changes; Load returns what the last Save wrote, or nothing
// on first run. Implementations must be safe for concurrent use.
type Store interface {
	Load() ([]Watch, error)
	Save([]Watch) error
}

// MemoryStore keeps watches for the life of the process only.
type MemoryStore struct {
	mu      sync.Mutex
	watches []Watch
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// Load returns a copy of the last Save.
func (m *MemoryStore) Load() ([]Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Watch(nil), m.watches...), nil
}

// Save replaces the stored watches.
func (m *MemoryStore) Save(ws []Watch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watches = append([]Watch(nil), ws...)
	return nil
}

// FileStore keeps watches in one JSON file. It writes to a temporary file in
// the same directory and renames it over the old one, so a crash mid-save
// leaves the previous state intact. The file holds every watch's webhook
// secret, so it is created 0600.
type FileStore struct {
	mu   sync.Mutex
	path string
}

// NewFileStore returns a FileStore at path. The file need not exist yet.
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

type fileState struct {
	Version int     `json:"version"`
	Watches []Watch `json:"watches"`
}

// Load reads the file. A missing file is an empty store, not an error.
func (f *FileStore) Load() ([]Watch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st fileState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decode %s: %w", f.path, err)
	}
	if st.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d", f.path, st.Version)
	}
	return st.Watches, nil
}

// Save writes every watch, replacing the file atomically.
func (f *FileStore) Save(ws []Watch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ws == nil {
		ws = []Watch{}
	}
	raw, err := json.MarshalIndent(fileState{Version: 1, Watches: ws}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".bridge-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}
