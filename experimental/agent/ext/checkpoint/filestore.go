package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

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
	files map[string]entry
}

// Info is a checkpoint's header, for listing without loading the manifest
// body.
type Info struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Files   int       `json:"files"`
}

// kind is what Add found at a path, which decides what restoring it means and
// whether restoring it is safe at all.
type kind string

const (
	// kindRegular is an ordinary file whose content was captured.
	kindRegular kind = "regular"

	// kindAbsent is a path that did not exist. Restoring it deletes whatever
	// the call went on to create, which is what undoing a creation means.
	kindAbsent kind = "absent"

	// kindUnsupported is anything else at capture time: a symlink, a
	// directory, a device. No content is captured and restoring is refused.
	//
	// Recorded rather than skipped, because a path the caller asked to protect
	// and that was silently never protected is the failure A11's corollary is
	// about. Naming it at /undo time is what lets someone notice.
	kindUnsupported kind = "unsupported"
)

// entry is what one captured path holds. It is a struct rather than a bare
// hash because the hash alone cannot express "this was not a regular file",
// and C2 rules out carrying that in a second map keyed by the same paths.
type entry struct {
	// Hash names the blob, and is empty for every kind but kindRegular.
	Hash string `json:"hash,omitempty"`

	// Kind is what was there at capture. Restore compares it against what is
	// there now, and refuses when they disagree.
	Kind kind `json:"kind"`
}

// manifestVersion is the on-disk format. Version 1 replaced a bare
// path-to-hash map, which could not distinguish a file that was absent from
// one that was a symlink, so a restore wrote through the link.
//
// Checkpoints are per-session artifacts under a working directory rather than
// durable state, so an older manifest is reported and ignored rather than
// migrated.
const manifestVersion = 1

type manifest struct {
	Version int              `json:"version"`
	ID      string           `json:"id"`
	Created time.Time        `json:"created"`
	Files   map[string]entry `json:"files"`
}

// Refusal is one path Restore declined to touch, and why.
type Refusal struct {
	// Path is the captured path, as recorded at capture time.
	Path string

	// Reason is a sentence for a human, naming what changed. It is meant to
	// be read in a /undo report, not matched on.
	Reason string
}

// RestoreResult reports what a Restore did and, more importantly, what it did
// not.
//
// Restore returns this rather than a bare error because a partial restore is
// a normal outcome here, not a failure: one tampered path should not block
// recovering the rest of the turn. But a caller that only learned "no error"
// would believe the turn was fully reversed, and constraint A11's corollary
// is precisely that a reversal path must report what it could not reverse.
type RestoreResult struct {
	// Restored lists the paths returned to their captured state, sorted.
	Restored []string

	// Refused lists the paths left alone, sorted by path. Empty means the
	// restore was complete.
	Refused []Refusal
}

// Complete reports whether every captured path was restored.
func (r RestoreResult) Complete() bool { return len(r.Refused) == 0 }

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
	return &Checkpoint{store: s, id: id, created: time.Now().UTC(), files: map[string]entry{}}, nil
}

// Load reads an existing checkpoint. The error satisfies os.IsNotExist when no
// checkpoint with this id was ever written.
func (s *Store) Load(id string) (*Checkpoint, error) {
	raw, err := os.ReadFile(s.manifestPath(id))
	if err != nil {
		return nil, err
	}
	// Version is read first so a manifest from the previous format reports
	// what it is. Unmarshalling it as the current one fails on a type error
	// and would be reported as corruption, sending someone to look for a
	// truncated write that never happened.
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("checkpoint: manifest %q is corrupt: %w", id, err)
	}
	if probe.Version != manifestVersion {
		return nil, fmt.Errorf("checkpoint: manifest %q is format v%d, this build writes v%d; delete it or keep using the mcpkit that wrote it", id, probe.Version, manifestVersion)
	}

	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("checkpoint: manifest %q is corrupt: %w", id, err)
	}
	if m.Files == nil {
		m.Files = map[string]entry{}
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
		e, err := c.store.capture(abs)
		if err != nil {
			return err
		}
		c.files[abs] = e
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
func (c *Checkpoint) Restore() (RestoreResult, error) {
	c.mu.Lock()
	files := make(map[string]entry, len(c.files))
	for p, e := range c.files {
		files[p] = e
	}
	c.mu.Unlock()

	type staged struct{ tmp, dst string }
	var writes []staged
	var deletes []string
	var result RestoreResult

	cleanup := func() {
		for _, w := range writes {
			os.Remove(w.tmp)
		}
	}
	refuse := func(path, reason string) {
		result.Refused = append(result.Refused, Refusal{Path: path, Reason: reason})
	}

	for _, dst := range sortedKeys(files) {
		e := files[dst]

		// What is there NOW, without following a link. The gap between
		// capture and /undo is a user deciding to type it, so this is not a
		// tight race: minutes or hours in which a path can become something
		// else entirely.
		now, err := os.Lstat(dst)
		switch {
		case err != nil && !os.IsNotExist(err):
			cleanup()
			return RestoreResult{}, fmt.Errorf("checkpoint: inspect %s: %w", dst, err)
		case err == nil && !now.Mode().IsRegular():
			// Covers both directions that matter: writing back through a
			// symlink that appeared, and deleting through one.
			refuse(dst, fmt.Sprintf("is now a %s, was %s at capture", describe(now.Mode()), e.Kind))
			continue
		}

		switch e.Kind {
		case kindUnsupported:
			refuse(dst, "was not a regular file at capture; checkpoint restores regular files only")
		case kindAbsent:
			deletes = append(deletes, dst)
		case kindRegular:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				cleanup()
				return RestoreResult{}, fmt.Errorf("checkpoint: prepare %s: %w", dst, err)
			}
			tmp, err := c.store.stage(e.Hash, dst)
			if err != nil {
				cleanup()
				return RestoreResult{}, err
			}
			writes = append(writes, staged{tmp: tmp, dst: dst})
		default:
			refuse(dst, fmt.Sprintf("unknown capture kind %q", e.Kind))
		}
	}

	for _, w := range writes {
		if err := os.Rename(w.tmp, w.dst); err != nil {
			return result, fmt.Errorf("checkpoint: restore %s (partially applied, safe to retry): %w", w.dst, err)
		}
		result.Restored = append(result.Restored, w.dst)
	}
	for _, dst := range deletes {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("checkpoint: remove %s (partially applied, safe to retry): %w", dst, err)
		}
		result.Restored = append(result.Restored, dst)
	}
	sort.Strings(result.Restored)
	return result, nil
}

// describe names a file type for a refusal message, since "is now a mode
// 0xa000" tells the reader nothing about what to go and look at.
func describe(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsDir():
		return "directory"
	case m&fs.ModeDevice != 0:
		return "device"
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	default:
		return "non-regular file"
	}
}

func sortedKeys(m map[string]entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Reversal presents this checkpoint through the seam, so a file-touching tool
// reverses the same way any other does. Restore-only: nothing here reaches
// outside the machine, which is exactly the property that makes it safe for
// the harness to run unattended.
func (c *Checkpoint) Reversal() Reversal {
	return Reversal{Restore: func(context.Context) error {
		res, err := c.Restore()
		if err != nil {
			return err
		}
		// A refusal is an error at this seam even though Restore treats it as
		// a normal partial outcome. The seam's caller is the harness running
		// unattended (A11), and it has no channel to report detail on: the
		// only thing it can tell a human is that the reversal did not fully
		// happen. Swallowing it here is exactly the unreported hole A11's
		// corollary names. /undo goes through Restore directly and reports
		// each refusal in full.
		if !res.Complete() {
			return fmt.Errorf("checkpoint: %d of %d path(s) could not be restored: %s",
				len(res.Refused), len(res.Refused)+len(res.Restored), res.Refused[0].Reason)
		}
		return nil
	}}
}

func (c *Checkpoint) save() error {
	m := manifest{Version: manifestVersion, ID: c.id, Created: c.created, Files: c.files}
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

// capture records what is at a path right now: its content if it is a regular
// file, that it was missing, or that it was something restoring cannot
// faithfully undo.
//
// The kind is recorded rather than inferred later because "what was here"
// stops being knowable the moment the tool runs, and it is the only thing
// Restore can compare against to notice a path changed shape underneath it.
func (s *Store) capture(path string) (entry, error) {
	// Lstat, not Stat: a symlink here must be recorded as one rather than
	// followed. Following it captures the target's content and, worse, makes
	// the restore write back through the link to a file the caller never
	// named.
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return entry{Kind: kindAbsent}, nil
	}
	if err != nil {
		return entry{}, fmt.Errorf("checkpoint: inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return entry{Kind: kindUnsupported}, nil
	}

	hash, err := s.putFile(path)
	if err != nil {
		return entry{}, err
	}
	return entry{Hash: hash, Kind: kindRegular}, nil
}

// putFile stores a file's content and returns its hash. An already-stored
// blob is left alone, which is where the dedup comes from.
func (s *Store) putFile(path string) (string, error) {
	data, err := os.ReadFile(path)
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
