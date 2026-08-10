package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// absent is the manifest hash for a path that did not exist when it was
// captured. Restoring it deletes the file, which is what undoing a create
// means. A sha256 hex string is never empty, so the sentinel cannot collide
// with a real blob.
const absent = ""

// Store is a content-addressed snapshot of files, on disk under one root.
//
// Content addressing is what makes repeated checkpoints affordable: a file
// captured in twenty turns and edited in one is stored twice, not twenty
// times, because nineteen captures hash to the same blob. That matters because
// the natural usage is a checkpoint per turn for as long as a session runs.
//
// Layout:
//
//	<root>/blobs/ab/cdef...      file contents, named by sha256, 2-char shard
//	<root>/manifests/<id>.json   {path -> hash} plus creation time
//
// A shadow git repo would give diffing and history for free and was rejected:
// it assumes the git binary, inherits .gitignore semantics that have nothing
// to do with what an agent touched, and has undefined behaviour against a
// working tree that is itself mid-rebase.
type Store struct {
	root string
}

// NewStore opens or creates a store rooted at dir.
func NewStore(dir string) (*Store, error) {
	for _, sub := range []string{"blobs", "manifests"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("checkpoint: create store at %s: %w", dir, err)
		}
	}
	return &Store{root: dir}, nil
}

// Checkpoint is one restore point, accumulating captured paths as a turn runs.
// Safe for concurrent Add: the Runner dispatches parallel tool calls, so two
// of them may capture at once.
type Checkpoint struct {
	store   *Store
	id      string
	created time.Time

	mu    sync.Mutex
	files map[string]string
}

// Info is a checkpoint's header, for listing without loading the manifest
// body.
type Info struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Files   int       `json:"files"`
}

type manifest struct {
	ID      string            `json:"id"`
	Created time.Time         `json:"created"`
	Files   map[string]string `json:"files"`
}

// Open returns the checkpoint with this id, loading it if it already exists.
// Reopening is how a turn that spans several tool calls keeps adding to one
// restore point.
func (s *Store) Open(id string) (*Checkpoint, error) {
	if id == "" {
		return nil, fmt.Errorf("checkpoint: id is required")
	}
	cp, err := s.Load(id)
	if err == nil {
		return cp, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	return &Checkpoint{store: s, id: id, created: time.Now().UTC(), files: map[string]string{}}, nil
}

// Load reads an existing checkpoint. The error satisfies os.IsNotExist when no
// checkpoint with this id was ever written.
func (s *Store) Load(id string) (*Checkpoint, error) {
	raw, err := os.ReadFile(s.manifestPath(id))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("checkpoint: manifest %q is corrupt: %w", id, err)
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return &Checkpoint{store: s, id: m.ID, created: m.Created, files: m.Files}, nil
}

// List returns every checkpoint's header, newest first.
func (s *Store) List() ([]Info, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "manifests"))
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		cp, err := s.Load(name[:len(name)-len(".json")])
		if err != nil {
			return nil, err
		}
		out = append(out, Info{ID: cp.id, Created: cp.created, Files: len(cp.files)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// ID reports the checkpoint's identifier.
func (c *Checkpoint) ID() string { return c.id }

// Paths reports the captured paths in sorted order.
func (c *Checkpoint) Paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.files))
	for p := range c.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Add captures each path's current content and persists the manifest.
//
// First capture wins: a path already in this checkpoint is left alone, so the
// checkpoint holds the state at the START of the turn rather than before the
// most recent write. Re-capturing on every write would make a restore point
// that undoes one edit instead of the turn.
//
// A path that does not exist is recorded as absent, so restoring deletes
// whatever the call went on to create. Without that, undoing a file creation
// would silently leave the new file in place.
//
// Paths are stored as given after resolving to absolute, so a checkpoint is
// valid on the machine that took it and is not portable off it.
func (c *Checkpoint) Add(paths ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	changed := false
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("checkpoint: resolve %q: %w", p, err)
		}
		if _, seen := c.files[abs]; seen {
			continue
		}
		hash, err := c.store.putFile(abs)
		if err != nil {
			return err
		}
		c.files[abs] = hash
		changed = true
	}
	if !changed {
		return nil
	}
	return c.save()
}

// Restore returns every captured path to the content Add saw: writing back
// what existed, deleting what did not.
//
// It works in two phases. Everything is staged to a temp file beside its
// destination first, so a missing blob or an unwritable directory fails before
// any destination has been touched. Only then does it rename each into place.
//
// That is NOT atomic across several files — POSIX has no multi-file rename —
// and claiming otherwise would be the kind of guarantee that reads as safety
// while providing none. What it is instead is idempotent and retryable:
// restoring the same checkpoint twice reaches the same state, so a restore
// that fails halfway is fixed by running it again rather than by manual
// repair. The manifest is never consumed, so retrying is always possible.
func (c *Checkpoint) Restore() error {
	c.mu.Lock()
	files := make(map[string]string, len(c.files))
	for p, h := range c.files {
		files[p] = h
	}
	c.mu.Unlock()

	type staged struct{ tmp, dst string }
	var writes []staged
	var deletes []string

	cleanup := func() {
		for _, w := range writes {
			os.Remove(w.tmp)
		}
	}

	for dst, hash := range files {
		if hash == absent {
			deletes = append(deletes, dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			cleanup()
			return fmt.Errorf("checkpoint: prepare %s: %w", dst, err)
		}
		tmp, err := c.store.stage(hash, dst)
		if err != nil {
			cleanup()
			return err
		}
		writes = append(writes, staged{tmp: tmp, dst: dst})
	}

	for _, w := range writes {
		if err := os.Rename(w.tmp, w.dst); err != nil {
			return fmt.Errorf("checkpoint: restore %s (partially applied, safe to retry): %w", w.dst, err)
		}
	}
	for _, dst := range deletes {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checkpoint: remove %s (partially applied, safe to retry): %w", dst, err)
		}
	}
	return nil
}

// Reversal presents this checkpoint through the seam, so a file-touching tool
// reverses the same way any other does. Restore-only: nothing here reaches
// outside the machine, which is exactly the property that makes it safe for
// the harness to run unattended.
func (c *Checkpoint) Reversal() Reversal {
	return Reversal{Restore: func(context.Context) error { return c.Restore() }}
}

func (c *Checkpoint) save() error {
	m := manifest{ID: c.id, Created: c.created, Files: c.files}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: encode manifest %q: %w", c.id, err)
	}
	path := c.store.manifestPath(c.id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("checkpoint: write manifest %q: %w", c.id, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("checkpoint: commit manifest %q: %w", c.id, err)
	}
	return nil
}

func (s *Store) manifestPath(id string) string {
	return filepath.Join(s.root, "manifests", id+".json")
}

func (s *Store) blobPath(hash string) string {
	return filepath.Join(s.root, "blobs", hash[:2], hash[2:])
}

// putFile stores a file's content and returns its hash, or absent when the
// file does not exist. An already-stored blob is left alone, which is where
// the dedup comes from.
func (s *Store) putFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return absent, nil
	}
	if err != nil {
		return "", fmt.Errorf("checkpoint: read %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dst := s.blobPath(hash)
	if _, err := os.Stat(dst); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("checkpoint: create blob dir: %w", err)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("checkpoint: write blob: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("checkpoint: commit blob: %w", err)
	}
	return hash, nil
}

// stage copies a blob to a temp file beside dst, ready to be renamed into
// place. Beside rather than in the system temp dir so the rename stays within
// one filesystem and cannot degrade into a copy.
func (s *Store) stage(hash, dst string) (string, error) {
	src, err := os.Open(s.blobPath(hash))
	if err != nil {
		return "", fmt.Errorf("checkpoint: blob for %s is missing: %w", dst, err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".checkpoint-*")
	if err != nil {
		return "", fmt.Errorf("checkpoint: stage %s: %w", dst, err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("checkpoint: stage %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("checkpoint: stage %s: %w", dst, err)
	}
	return tmp.Name(), nil
}
