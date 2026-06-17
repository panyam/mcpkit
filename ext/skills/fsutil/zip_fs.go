// Package fsutil provides generic io/fs.FS adapters used by ext/skills
// — disk-backed zip access, layered fs.FS composition, etc. Nothing in
// this package depends on skills semantics; it lives here while
// ext/skills is the only consumer. Once a second consumer surfaces it
// will be promoted to mcpkit/fsutil/ at the root (and eventually
// pushed down to a shared lib alongside templar). See the migration
// note in package skills's doc.go.
package fsutil

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"time"
)

// ZipFS adapts a *zip.ReadCloser to io/fs.FS + io.Closer. Unlike an
// in-memory archive view, ZipFS keeps the central directory mapped
// via archive/zip and streams per-file reads from disk on demand:
// memory footprint is bounded by the size of any single file being
// read, not the whole archive. Tar formats can't do this — tar is a
// streaming format with no central directory — but zip has random
// access by design, so this is the natural shape for it.
//
// Callers should defer fs.Close() (ZipFS implements both fs.FS and
// io.Closer so the standard "open + defer close" pattern works
// directly).
type ZipFS struct {
	rc *zip.ReadCloser
}

// OpenZipFS opens a zip archive from disk and returns a ZipFS. The
// underlying file handle stays open until Close is called.
func OpenZipFS(path string) (*ZipFS, error) {
	rc, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("fsutil: open zip %q: %w", path, err)
	}
	return &ZipFS{rc: rc}, nil
}

// NewZipFS wraps an already-open *zip.ReadCloser. Useful when the
// caller obtained the reader through a non-disk path (HTTP body,
// embedded bytes). Close on the returned ZipFS closes the reader.
func NewZipFS(rc *zip.ReadCloser) *ZipFS {
	return &ZipFS{rc: rc}
}

// Open implements fs.FS. Returns fs.ErrNotExist for paths not present
// in the central directory. Filenames in zips are forward-slash
// separated per the zip format; callers should pass the same.
func (z *ZipFS) Open(name string) (fs.File, error) {
	if name == "" || name == "." {
		return &zipDir{name: ".", z: z}, nil
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	for _, f := range z.rc.File {
		clean := zipCleanName(f.Name)
		if clean == name {
			if f.FileInfo().IsDir() {
				return &zipDir{name: clean, z: z}, nil
			}
			rc, err := f.Open()
			if err != nil {
				return nil, &fs.PathError{Op: "open", Path: name, Err: err}
			}
			return &zipFile{name: clean, info: f.FileInfo(), rc: rc}, nil
		}
	}
	// Synthesize a directory file if any entry has name as a parent.
	prefix := name + "/"
	for _, f := range z.rc.File {
		clean := zipCleanName(f.Name)
		if clean == name {
			continue
		}
		if len(clean) > len(prefix) && clean[:len(prefix)] == prefix {
			return &zipDir{name: name, z: z}, nil
		}
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Close releases the underlying zip.ReadCloser and file handle. Safe
// to call multiple times; subsequent calls return nil. After Close,
// Open calls error with the underlying file-closed error.
func (z *ZipFS) Close() error {
	if z.rc == nil {
		return nil
	}
	err := z.rc.Close()
	z.rc = nil
	return err
}

func zipCleanName(name string) string {
	for len(name) > 0 && name[len(name)-1] == '/' {
		name = name[:len(name)-1]
	}
	return name
}

type zipFile struct {
	name string
	info fs.FileInfo
	rc   io.ReadCloser
	pos  int64
}

func (f *zipFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *zipFile) Read(p []byte) (int, error) {
	n, err := f.rc.Read(p)
	f.pos += int64(n)
	return n, err
}
func (f *zipFile) Close() error { return f.rc.Close() }

type zipDir struct {
	name string
	z    *ZipFS
	pos  int
}

func (d *zipDir) Stat() (fs.FileInfo, error) {
	return zipDirInfo{name: pathBase(d.name)}, nil
}
func (d *zipDir) Read([]byte) (int, error) { return 0, fmt.Errorf("read on directory %q", d.name) }
func (d *zipDir) Close() error             { return nil }

func (d *zipDir) ReadDir(n int) ([]fs.DirEntry, error) {
	prefix := ""
	if d.name != "." {
		prefix = d.name + "/"
	}
	seen := map[string]bool{}
	var out []fs.DirEntry
	for _, f := range d.z.rc.File {
		clean := zipCleanName(f.Name)
		if clean == d.name {
			continue
		}
		if prefix == "" {
			rest := clean
			if i := indexByteRune(rest, '/'); i >= 0 {
				rest = rest[:i]
			}
			if rest == "" || seen[rest] {
				continue
			}
			seen[rest] = true
			out = append(out, zipDirEntry{name: rest, isDir: rest != clean || f.FileInfo().IsDir()})
			continue
		}
		if len(clean) <= len(prefix) || clean[:len(prefix)] != prefix {
			continue
		}
		rest := clean[len(prefix):]
		if i := indexByteRune(rest, '/'); i >= 0 {
			rest = rest[:i]
		}
		if rest == "" || seen[rest] {
			continue
		}
		seen[rest] = true
		full := prefix + rest
		out = append(out, zipDirEntry{name: rest, isDir: full != clean || f.FileInfo().IsDir()})
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	if n > 0 && len(out) == 0 {
		return nil, io.EOF
	}
	return out, nil
}

func indexByteRune(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func pathBase(p string) string {
	if i := lastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

type zipDirEntry struct {
	name  string
	isDir bool
}

func (e zipDirEntry) Name() string { return e.name }
func (e zipDirEntry) IsDir() bool  { return e.isDir }
func (e zipDirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e zipDirEntry) Info() (fs.FileInfo, error) {
	if e.isDir {
		return zipDirInfo{name: e.name}, nil
	}
	return zipFileInfo{name: e.name}, nil
}

type zipDirInfo struct{ name string }

func (i zipDirInfo) Name() string       { return i.name }
func (i zipDirInfo) Size() int64        { return 0 }
func (i zipDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (i zipDirInfo) ModTime() time.Time { return time.Time{} }
func (i zipDirInfo) IsDir() bool        { return true }
func (i zipDirInfo) Sys() any           { return nil }

type zipFileInfo struct{ name string }

func (i zipFileInfo) Name() string       { return i.name }
func (i zipFileInfo) Size() int64        { return 0 }
func (i zipFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i zipFileInfo) ModTime() time.Time { return time.Time{} }
func (i zipFileInfo) IsDir() bool        { return false }
func (i zipFileInfo) Sys() any           { return nil }
