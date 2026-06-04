package skills

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// ArchiveFS adapts archive bytes (.tar.gz or .zip) to an io/fs.FS view.
// Use it on the source side of a Provider when a server's skills live
// inside an archive on disk, or on the client side when consuming a
// type:"archive" resource and wanting to walk the contents as if they
// were files.
//
// Construction unpacks the archive once into a slice of UnpackedEntry,
// enforcing the SEP-2640 safety rules on every entry. After
// construction, Open and ReadFile are O(log n) via a sorted index. The
// implementation does not stream from the original bytes; ArchiveFS is
// not appropriate for archives whose unpacked content does not fit
// comfortably in memory. Callers with that constraint should use
// UnpackBytes and stage to disk.
type ArchiveFS struct {
	entries []UnpackedEntry
	dirs    map[string]bool
	// modTime is the synthetic mtime ArchiveFS reports for every entry.
	// Archive formats do not carry a consistent per-entry mtime across
	// hosts, and the Indexer's mtime-based cache invalidation reads
	// this value. NewArchiveFS sets it to the time the archive was
	// decoded; callers that need a different value can adjust via the
	// returned struct.
	modTime time.Time
}

// NewArchiveFS unpacks archive bytes and returns a read-only fs.FS view.
// The format is detected from the supplied hint when non-Unknown;
// otherwise NewArchiveFS sniffs the magic bytes via
// DetectArchiveFormat. The maxBytes cap is the total unpacked size; 0
// uses DefaultArchiveMaxBytes.
//
// Safety rules from SEP-2640 are enforced at decode time. Any
// path-traversal, absolute-path, or symlink-escape entry causes
// NewArchiveFS to return the matching named error before any file is
// readable.
func NewArchiveFS(data []byte, format ArchiveFormat, maxBytes int64) (*ArchiveFS, error) {
	entries, err := UnpackBytes(data, format, maxBytes)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	dirs := map[string]bool{".": true}
	for _, e := range entries {
		parent := path.Dir(e.Path)
		for parent != "" && parent != "." && !dirs[parent] {
			dirs[parent] = true
			parent = path.Dir(parent)
		}
	}
	return &ArchiveFS{
		entries: entries,
		dirs:    dirs,
		modTime: time.Now(),
	}, nil
}

// Open implements fs.FS.
func (a *ArchiveFS) Open(name string) (fs.File, error) {
	if name == "" || name == "." {
		return &archiveDir{name: ".", a: a}, nil
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if a.dirs[name] {
		return &archiveDir{name: name, a: a}, nil
	}
	idx := sort.Search(len(a.entries), func(i int) bool {
		return a.entries[i].Path >= name
	})
	if idx < len(a.entries) && a.entries[idx].Path == name {
		return &archiveFile{entry: a.entries[idx], modTime: a.modTime}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Stat implements fs.StatFS. Reporting Stat directly (rather than
// having fs.Stat route through Open) keeps the Indexer's mtime check
// O(log n) per call instead of touching every file.
func (a *ArchiveFS) Stat(name string) (fs.FileInfo, error) {
	f, err := a.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// ReadDir implements fs.ReadDirFS so fs.WalkDir descends into archive
// directories efficiently.
func (a *ArchiveFS) ReadDir(name string) ([]fs.DirEntry, error) {
	prefix := name
	if prefix == "." || prefix == "" {
		prefix = ""
	} else if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	seen := map[string]bool{}
	var out []fs.DirEntry
	for _, e := range a.entries {
		if !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(e.Path, prefix)
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			out = append(out, &archiveDirEntry{name: rest, isDir: false, entry: e, modTime: a.modTime})
			continue
		}
		child := rest[:slash]
		if seen[child] {
			continue
		}
		seen[child] = true
		out = append(out, &archiveDirEntry{name: child, isDir: true, modTime: a.modTime})
	}
	if len(out) == 0 && name != "." && !a.dirs[name] {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// ReadFile implements fs.ReadFileFS so callers do not have to manually
// open + io.ReadAll for the common "I want the bytes" path.
func (a *ArchiveFS) ReadFile(name string) ([]byte, error) {
	idx := sort.Search(len(a.entries), func(i int) bool {
		return a.entries[i].Path >= name
	})
	if idx < len(a.entries) && a.entries[idx].Path == name {
		body := make([]byte, len(a.entries[idx].Body))
		copy(body, a.entries[idx].Body)
		return body, nil
	}
	return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
}

// --- supporting fs.File / fs.DirEntry / fs.FileInfo implementations ---

type archiveFile struct {
	entry   UnpackedEntry
	modTime time.Time
	r       *bytes.Reader
}

func (f *archiveFile) reader() *bytes.Reader {
	if f.r == nil {
		f.r = bytes.NewReader(f.entry.Body)
	}
	return f.r
}

func (f *archiveFile) Stat() (fs.FileInfo, error) {
	return &archiveFileInfo{
		name:    path.Base(f.entry.Path),
		size:    int64(len(f.entry.Body)),
		mode:    f.entry.Mode,
		modTime: f.modTime,
	}, nil
}

func (f *archiveFile) Read(p []byte) (int, error) {
	return f.reader().Read(p)
}

func (f *archiveFile) Close() error { return nil }

type archiveDir struct {
	name    string
	a       *ArchiveFS
	entries []fs.DirEntry
	pos     int
}

func (d *archiveDir) Stat() (fs.FileInfo, error) {
	return &archiveFileInfo{
		name:    path.Base(d.name),
		size:    0,
		mode:    fs.ModeDir | 0o555,
		modTime: d.a.modTime,
	}, nil
}

func (d *archiveDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errIsDirectory}
}

func (d *archiveDir) Close() error { return nil }

func (d *archiveDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.entries == nil {
		ents, err := d.a.ReadDir(d.name)
		if err != nil {
			return nil, err
		}
		d.entries = ents
	}
	if n <= 0 {
		out := d.entries[d.pos:]
		d.pos = len(d.entries)
		return out, nil
	}
	remaining := len(d.entries) - d.pos
	if remaining == 0 {
		return nil, io.EOF
	}
	if n > remaining {
		n = remaining
	}
	out := d.entries[d.pos : d.pos+n]
	d.pos += n
	return out, nil
}

var errIsDirectory = errors.New("is a directory")

type archiveDirEntry struct {
	name    string
	isDir   bool
	entry   UnpackedEntry
	modTime time.Time
}

func (e *archiveDirEntry) Name() string { return e.name }
func (e *archiveDirEntry) IsDir() bool  { return e.isDir }
func (e *archiveDirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e *archiveDirEntry) Info() (fs.FileInfo, error) {
	if e.isDir {
		return &archiveFileInfo{
			name:    e.name,
			size:    0,
			mode:    fs.ModeDir | 0o555,
			modTime: e.modTime,
		}, nil
	}
	return &archiveFileInfo{
		name:    e.name,
		size:    int64(len(e.entry.Body)),
		mode:    e.entry.Mode,
		modTime: e.modTime,
	}, nil
}

type archiveFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

func (i *archiveFileInfo) Name() string       { return i.name }
func (i *archiveFileInfo) Size() int64        { return i.size }
func (i *archiveFileInfo) Mode() fs.FileMode  { return i.mode }
func (i *archiveFileInfo) ModTime() time.Time { return i.modTime }
func (i *archiveFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *archiveFileInfo) Sys() any           { return nil }
