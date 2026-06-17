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

// Layer is one input to NewLayered / NewFlat: an fs.FS plus the
// prefix it should be mounted under (layered mode) or the empty
// string (flat mode, where contents merge at the root).
type Layer struct {
	// Prefix is the path segment this layer's contents are mounted
	// under in layered mode. Ignored when the parent LayeredFS is in
	// flat mode.
	Prefix string

	// FSys is the layer's underlying fs.FS. Reads on the LayeredFS
	// delegate here for paths under Prefix (layered) or for any path
	// the layer carries (flat).
	FSys fs.FS

	// Closer, when non-nil, is invoked from LayeredFS.Close. Use this
	// to hand off cleanup of disk-backed sources (ZipFS, tempfile
	// streams) to the layer composition's lifecycle.
	Closer io.Closer
}

// LayeredFS combines multiple per-layer fs.FS instances into one
// virtual filesystem. Two layouts:
//
//   - Layered (NewLayered): each layer is mounted under its Prefix.
//     The synthetic root lists one entry per Prefix. Reads under
//     <Prefix>/... delegate to the layer's fs.FS with the prefix
//     stripped.
//   - Flat (NewFlat): every layer's contents merge at the root. The
//     constructor errors on path collision so first-hit-wins reads
//     are unambiguous.
//
// LayeredFS implements fs.FS + io.Closer. Closing closes every
// layer's Closer (if any), in registration order; the first error
// is returned.
type LayeredFS struct {
	layers []Layer
	flat   bool
}

// NewLayered mounts each layer under its Prefix. The synthetic root
// lists one entry per Prefix; paths under <Prefix>/... delegate to
// the layer's fs.FS with the prefix stripped. Prefix collisions
// across layers are not allowed and return an error.
func NewLayered(layers ...Layer) (*LayeredFS, error) {
	seen := map[string]bool{}
	for _, l := range layers {
		if l.Prefix == "" {
			return nil, errors.New("fsutil: NewLayered requires non-empty Layer.Prefix; use NewFlat for flat composition")
		}
		if seen[l.Prefix] {
			return nil, fmt.Errorf("fsutil: NewLayered duplicate prefix %q", l.Prefix)
		}
		seen[l.Prefix] = true
	}
	return &LayeredFS{layers: layers}, nil
}

// NewFlat merges every layer's contents at the root. Errors on
// path collision — two layers contributing the same root-level
// path is rejected at construction so first-hit-wins reads are
// unambiguous.
//
// The collision check walks each layer's root listing; collisions
// deeper in the tree are not pre-checked (they would require
// fully walking every layer, which defeats lazy reads). Callers
// expecting deep collisions should prefer NewLayered + use
// per-layer prefixes.
func NewFlat(layers ...Layer) (*LayeredFS, error) {
	roots := map[string]bool{}
	for _, l := range layers {
		root, err := l.FSys.Open(".")
		if err != nil {
			return nil, fmt.Errorf("fsutil: NewFlat open root of layer: %w", err)
		}
		dirReader, ok := root.(fs.ReadDirFile)
		if !ok {
			root.Close()
			continue
		}
		children, err := dirReader.ReadDir(-1)
		root.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("fsutil: NewFlat read root of layer: %w", err)
		}
		for _, c := range children {
			if roots[c.Name()] {
				return nil, fmt.Errorf("fsutil: NewFlat root-level collision on %q", c.Name())
			}
			roots[c.Name()] = true
		}
	}
	return &LayeredFS{layers: layers, flat: true}, nil
}

// Open implements fs.FS.
func (l *LayeredFS) Open(name string) (fs.File, error) {
	if name == "" || name == "." {
		if l.flat {
			return l.openFlatRoot()
		}
		return &layeredRootDir{layers: l.layers}, nil
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if l.flat {
		return l.openFlat(name)
	}
	return l.openLayered(name)
}

func (l *LayeredFS) openLayered(name string) (fs.File, error) {
	segs := strings.SplitN(name, "/", 2)
	prefix := segs[0]
	for _, layer := range l.layers {
		if layer.Prefix != prefix {
			continue
		}
		rest := "."
		if len(segs) == 2 {
			rest = segs[1]
		}
		return layer.FSys.Open(rest)
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (l *LayeredFS) openFlat(name string) (fs.File, error) {
	for _, layer := range l.layers {
		f, err := layer.FSys.Open(name)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (l *LayeredFS) openFlatRoot() (fs.File, error) {
	seen := map[string]bool{}
	var entries []fs.DirEntry
	for _, layer := range l.layers {
		root, err := layer.FSys.Open(".")
		if err != nil {
			return nil, err
		}
		dirReader, ok := root.(fs.ReadDirFile)
		if !ok {
			root.Close()
			continue
		}
		children, err := dirReader.ReadDir(-1)
		root.Close()
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
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return &layeredFlatRoot{entries: entries}, nil
}

// Close releases every layer's Closer (if any), in registration
// order. Returns the first non-nil error; remaining layers still
// get their Close called. Safe to call multiple times.
func (l *LayeredFS) Close() error {
	var first error
	for i := range l.layers {
		if l.layers[i].Closer == nil {
			continue
		}
		if err := l.layers[i].Closer.Close(); err != nil && first == nil {
			first = err
		}
		l.layers[i].Closer = nil
	}
	return first
}

type layeredRootDir struct {
	layers []Layer
	pos    int
}

func (d *layeredRootDir) Stat() (fs.FileInfo, error) {
	return layeredDirInfo{name: "."}, nil
}
func (d *layeredRootDir) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read on directory %q", ".")
}
func (d *layeredRootDir) Close() error { return nil }

func (d *layeredRootDir) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := d.layers[d.pos:]
	if n > 0 && n < len(remaining) {
		remaining = remaining[:n]
	}
	out := make([]fs.DirEntry, 0, len(remaining))
	for _, layer := range remaining {
		out = append(out, layeredDirEntry{name: layer.Prefix, isDir: true})
	}
	d.pos += len(remaining)
	if n > 0 && len(out) == 0 {
		return nil, io.EOF
	}
	return out, nil
}

type layeredFlatRoot struct {
	entries []fs.DirEntry
	pos     int
}

func (d *layeredFlatRoot) Stat() (fs.FileInfo, error) {
	return layeredDirInfo{name: "."}, nil
}
func (d *layeredFlatRoot) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read on directory %q", ".")
}
func (d *layeredFlatRoot) Close() error { return nil }

func (d *layeredFlatRoot) ReadDir(n int) ([]fs.DirEntry, error) {
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

type layeredDirEntry struct {
	name  string
	isDir bool
}

func (e layeredDirEntry) Name() string { return e.name }
func (e layeredDirEntry) IsDir() bool  { return e.isDir }
func (e layeredDirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e layeredDirEntry) Info() (fs.FileInfo, error) {
	return layeredDirInfo{name: e.name}, nil
}

type layeredDirInfo struct{ name string }

func (i layeredDirInfo) Name() string       { return i.name }
func (i layeredDirInfo) Size() int64        { return 0 }
func (i layeredDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (i layeredDirInfo) ModTime() time.Time { return time.Time{} }
func (i layeredDirInfo) IsDir() bool        { return true }
func (i layeredDirInfo) Sys() any           { return nil }
