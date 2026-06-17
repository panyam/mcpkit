package fsutil

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// MountFS composes multiple fs.FS instances into one virtual
// filesystem via prefix-based routing — the direct analog of Unix
// mount points or HTTP-router prefix mounts (chi's r.Mount, mux's
// PathPrefix, etc.). Each Open call routes the requested path to
// exactly one underlying fs.FS based on the path's first segment.
//
// # Composition rules
//
//   - Mount with non-empty Path: sub-mount at that prefix. Paths under
//     <Path>/... delegate to the mount's fs.FS with the prefix stripped.
//     Duplicate Paths error at construction.
//   - Mount with empty Path: root mount. Its contents appear at the
//     MountFS root (no prefix). Multiple empty-Path mounts compose
//     flat-merge style with collision detection at the root level.
//   - Cross-collision: a sub-mount's Path must not collide with a
//     root mount's root-level entry name. Errors at construction.
//
// # Hierarchical composition
//
// Because MountFS implements fs.FS, it can be passed as the FSys of
// a Mount in another MountFS. Path resolution recurses: each level
// strips its prefix and delegates one hop deeper. Closer propagation
// also recurses (passing the inner MountFS as both FSys and Closer
// ensures Close cascades through the tree).
//
//	leaf  := NewMountFS(Mount{FSys: leafFS})
//	mid   := NewMountFS(Mount{Path: "leaf", FSys: leaf, Closer: leaf})
//	top   := NewMountFS(Mount{Path: "mid", FSys: mid, Closer: mid})
//	// "mid/leaf/foo.txt" resolves to leafFS.Open("foo.txt")
//
// # Lifecycle
//
// MountFS implements io.Closer. Close invokes each Mount's Closer
// (if non-nil) in registration order, returning the first error
// encountered. Subsequent calls are no-ops.
type MountFS struct {
	roots    []Mount
	subs     []Mount
	subIndex map[string]int // Path → index into subs
}

// Mount is one component of a MountFS — either a root mount (empty
// Path) or a sub-mount at a non-empty Path. The FSys is the underlying
// filesystem; Closer (optional) is invoked when the parent MountFS is
// closed, useful for handing off cleanup of disk-backed sources
// (ZipFS, tempfile streams, etc.).
type Mount struct {
	// Path is the prefix this mount lives under. Empty = root mount.
	// Non-empty = sub-mount at that path segment.
	Path string

	// FSys is the mount's underlying filesystem. May be any fs.FS,
	// including another MountFS for hierarchical composition.
	FSys fs.FS

	// Closer is invoked from MountFS.Close. Nil means the mount owns
	// no resources requiring cleanup.
	Closer io.Closer
}

// NewMountFS constructs a MountFS from the supplied mounts. Validates
// per the composition rules in the type doc:
//   - duplicate sub-mount Paths error
//   - multiple root mounts go through flat-merge collision detection
//     against each other's root-level entries
//   - sub-mount Paths must not collide with root mounts' root-level
//     entries
//
// Returns nil and an error on any construction failure; the caller is
// responsible for closing the provided mounts in that case (NewMountFS
// does not close them on its own failure path so the caller retains
// authority over the lifecycle).
func NewMountFS(mounts ...Mount) (*MountFS, error) {
	m := &MountFS{subIndex: map[string]int{}}
	for _, mt := range mounts {
		if mt.Path == "" {
			m.roots = append(m.roots, mt)
			continue
		}
		if strings.ContainsRune(mt.Path, '/') {
			return nil, fmt.Errorf("fsutil: Mount.Path %q must be a single path segment (no '/')", mt.Path)
		}
		if _, dup := m.subIndex[mt.Path]; dup {
			return nil, fmt.Errorf("fsutil: NewMountFS duplicate Path %q", mt.Path)
		}
		m.subIndex[mt.Path] = len(m.subs)
		m.subs = append(m.subs, mt)
	}
	if err := m.validateRootCollisions(); err != nil {
		return nil, err
	}
	return m, nil
}

// validateRootCollisions ensures:
//   - multiple root mounts don't carry conflicting root-level entry names
//   - a sub-mount's Path doesn't collide with a root mount's root-level
//     entry name
//
// Cost: O(sum of root layers' root-entry counts). Deeper collisions
// in root mounts aren't pre-checked — they'd require fully walking
// every root mount, which defeats lazy access. Callers expecting
// deep root-level conflicts should use sub-mounts instead.
func (m *MountFS) validateRootCollisions() error {
	if len(m.roots) == 0 {
		return nil
	}
	rootEntries := map[string]bool{}
	for _, root := range m.roots {
		rootDir, err := root.FSys.Open(".")
		if err != nil {
			return fmt.Errorf("fsutil: NewMountFS open root of mount: %w", err)
		}
		dirReader, ok := rootDir.(fs.ReadDirFile)
		if !ok {
			rootDir.Close()
			continue
		}
		children, err := dirReader.ReadDir(-1)
		rootDir.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("fsutil: NewMountFS read root of mount: %w", err)
		}
		for _, c := range children {
			if rootEntries[c.Name()] {
				return fmt.Errorf("fsutil: NewMountFS root-level collision on %q across multiple root mounts", c.Name())
			}
			rootEntries[c.Name()] = true
		}
	}
	for path := range m.subIndex {
		if rootEntries[path] {
			return fmt.Errorf("fsutil: NewMountFS sub-mount Path %q collides with a root mount's root-level entry", path)
		}
	}
	return nil
}

// Open implements fs.FS. Routes to the right mount based on the
// path's first segment: sub-mounts win over roots for matching
// segments; misses fall through to roots in registration order.
func (m *MountFS) Open(name string) (fs.File, error) {
	if name == "" || name == "." {
		return m.openRoot()
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	segs := strings.SplitN(name, "/", 2)
	first := segs[0]
	if idx, ok := m.subIndex[first]; ok {
		rest := "."
		if len(segs) == 2 {
			rest = segs[1]
		}
		return m.subs[idx].FSys.Open(rest)
	}
	for _, root := range m.roots {
		f, err := root.FSys.Open(name)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// openRoot returns a synthetic root directory listing every direct
// child of every root mount plus every sub-mount Path. Collisions
// were already detected at construction.
func (m *MountFS) openRoot() (fs.File, error) {
	var entries []fs.DirEntry
	seen := map[string]bool{}
	for _, root := range m.roots {
		rootDir, err := root.FSys.Open(".")
		if err != nil {
			return nil, err
		}
		dirReader, ok := rootDir.(fs.ReadDirFile)
		if !ok {
			rootDir.Close()
			continue
		}
		children, err := dirReader.ReadDir(-1)
		rootDir.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for _, c := range children {
			if seen[c.Name()] {
				continue
			}
			seen[c.Name()] = true
			entries = append(entries, c)
		}
	}
	for _, sub := range m.subs {
		if seen[sub.Path] {
			continue
		}
		seen[sub.Path] = true
		entries = append(entries, mountDirEntry{name: sub.Path, isDir: true})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return &mountRoot{entries: entries}, nil
}

// Close invokes each mount's Closer (if non-nil) in registration
// order. Returns the first non-nil error; remaining mounts still
// get their Close called. Safe to call multiple times.
func (m *MountFS) Close() error {
	var first error
	for i := range m.subs {
		if m.subs[i].Closer == nil {
			continue
		}
		if err := m.subs[i].Closer.Close(); err != nil && first == nil {
			first = err
		}
		m.subs[i].Closer = nil
	}
	for i := range m.roots {
		if m.roots[i].Closer == nil {
			continue
		}
		if err := m.roots[i].Closer.Close(); err != nil && first == nil {
			first = err
		}
		m.roots[i].Closer = nil
	}
	return first
}

type mountRoot struct {
	entries []fs.DirEntry
	pos     int
}

func (d *mountRoot) Stat() (fs.FileInfo, error) {
	return mountDirInfo{name: "."}, nil
}
func (d *mountRoot) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read on directory %q", ".")
}
func (d *mountRoot) Close() error { return nil }

func (d *mountRoot) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := d.entries[d.pos:]
	if n > 0 && n < len(remaining) {
		remaining = remaining[:n]
	}
	d.pos += len(remaining)
	if n > 0 && len(remaining) == 0 {
		return nil, io.EOF
	}
	return remaining, nil
}

type mountDirEntry struct {
	name  string
	isDir bool
}

func (e mountDirEntry) Name() string { return e.name }
func (e mountDirEntry) IsDir() bool  { return e.isDir }
func (e mountDirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e mountDirEntry) Info() (fs.FileInfo, error) {
	return mountDirInfo{name: e.name}, nil
}

type mountDirInfo struct{ name string }

func (i mountDirInfo) Name() string       { return i.name }
func (i mountDirInfo) Size() int64        { return 0 }
func (i mountDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (i mountDirInfo) ModTime() time.Time { return time.Time{} }
func (i mountDirInfo) IsDir() bool        { return true }
func (i mountDirInfo) Sys() any           { return nil }
