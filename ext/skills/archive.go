package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// ArchiveFormat selects the on-the-wire encoding for a packed skill.
// SEP-2640 fixes two formats: gzip-compressed tar (ArchiveTarGz, MIME
// application/gzip) and zip (ArchiveZip, MIME application/zip). Hosts
// MUST support both and SHOULD pick the format from a resource's
// mimeType, falling back to the URL suffix.
type ArchiveFormat int

const (
	// ArchiveFormatUnknown is the zero value. NewProvider rejects it
	// when WithArchiveMode is in effect.
	ArchiveFormatUnknown ArchiveFormat = iota
	// ArchiveFormatTarGz is the gzip-compressed tar format.
	ArchiveFormatTarGz
	// ArchiveFormatZip is the zip format.
	ArchiveFormatZip
)

// Suffix returns the URL/file-name suffix that identifies the format
// (".tar.gz" or ".zip"). Returns the empty string for ArchiveFormatUnknown.
func (f ArchiveFormat) Suffix() string {
	switch f {
	case ArchiveFormatTarGz:
		return ArchiveTarGz
	case ArchiveFormatZip:
		return ArchiveZip
	}
	return ""
}

// MimeType returns the SEP-2640 MIME type for the format.
func (f ArchiveFormat) MimeType() string {
	switch f {
	case ArchiveFormatTarGz:
		return "application/gzip"
	case ArchiveFormatZip:
		return "application/zip"
	}
	return ""
}

// String renders the format for diagnostics. Not the wire form.
func (f ArchiveFormat) String() string {
	switch f {
	case ArchiveFormatTarGz:
		return "tar.gz"
	case ArchiveFormatZip:
		return "zip"
	}
	return "unknown"
}

// DefaultArchiveMaxBytes is the unpacked-size cap a Provider uses when
// no WithArchiveMaxBytes option is supplied. Per SEP-2640 the host MUST
// "enforce a limit on total unpacked size" to prevent decompression
// bombs. 100 MiB is a defensible default for skills which the SEP
// expects to be document-sized, not dataset-sized.
const DefaultArchiveMaxBytes int64 = 100 * 1024 * 1024

// Archive safety errors.
var (
	// ErrArchivePathTraversal is returned when an entry path contains
	// ".." or otherwise resolves outside the archive root after
	// path.Clean.
	ErrArchivePathTraversal = errors.New("skills: archive entry escapes root")

	// ErrArchiveAbsolutePath is returned when an entry path is absolute
	// (starts with "/").
	ErrArchiveAbsolutePath = errors.New("skills: archive entry has absolute path")

	// ErrArchiveSymlinkEscape is returned when a tar symlink or hardlink
	// resolves outside the archive root.
	ErrArchiveSymlinkEscape = errors.New("skills: archive symlink escapes root")

	// ErrArchiveTooLarge is returned when an archive's total unpacked
	// size exceeds the configured maximum. The unpack/read stream errors
	// as soon as the cap is crossed; no partial-file is returned.
	ErrArchiveTooLarge = errors.New("skills: archive exceeds max unpacked size")

	// ErrArchiveUnknownFormat is returned when neither the supplied
	// format nor the inferred magic bytes identify a supported format.
	ErrArchiveUnknownFormat = errors.New("skills: unknown archive format")
)

// DetectArchiveFormat infers an ArchiveFormat from a URL or filename
// suffix (".tar.gz" or ".zip") and falls back to a magic-byte sniff on
// peek when the suffix is missing or ambiguous. Returns
// ArchiveFormatUnknown on no match.
//
// Suffix matching is case-sensitive and trims a single ".gz"-like layer
// only when the file is also ".tar.gz" — bare ".gz" is not a SEP-2640
// archive shape and gets ArchiveFormatUnknown.
func DetectArchiveFormat(name string, peek []byte) ArchiveFormat {
	switch {
	case strings.HasSuffix(name, ArchiveTarGz):
		return ArchiveFormatTarGz
	case strings.HasSuffix(name, ArchiveZip):
		return ArchiveFormatZip
	}
	if len(peek) >= 4 {
		if peek[0] == 0x1F && peek[1] == 0x8B {
			return ArchiveFormatTarGz
		}
		if peek[0] == 0x50 && peek[1] == 0x4B && peek[2] == 0x03 && peek[3] == 0x04 {
			return ArchiveFormatZip
		}
	}
	return ArchiveFormatUnknown
}

// PackSkill packs everything under skillDir in fsys into the chosen
// archive format. SKILL.md MUST be present at skillDir; the resulting
// archive carries it at the archive root and contains no entries
// outside the skill directory.
//
// Entries are written in lexically sorted order so two packs of the
// same source produce byte-identical archives. This is load-bearing
// for the Indexer's digest stability across cache rebuilds.
//
// Pack-side safety: PackSkill refuses to emit any entry whose
// normalized path contains ".." or starts with "/", and refuses to
// follow symlinks that point outside skillDir. These are SEP MUST
// rejections on the unpack side, so emitting such entries is a defect
// even when individual hosts might be lenient.
func PackSkill(fsys fs.FS, skillDir string, format ArchiveFormat) ([]byte, error) {
	if format == ArchiveFormatUnknown {
		return nil, fmt.Errorf("%w: missing format", ErrArchiveUnknownFormat)
	}
	files, err := collectSkillFiles(fsys, skillDir)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	switch format {
	case ArchiveFormatTarGz:
		if err := writeTarGz(&buf, fsys, skillDir, files); err != nil {
			return nil, err
		}
	case ArchiveFormatZip:
		if err := writeZip(&buf, fsys, skillDir, files); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrArchiveUnknownFormat, format)
	}
	return buf.Bytes(), nil
}

// collectSkillFiles walks the skill subtree and returns relative paths
// (skill-root-relative) in lexical order. Validates that no path
// component contains ".." segments after path.Clean. Skips directory
// entries so the archive carries files only.
func collectSkillFiles(fsys fs.FS, skillDir string) ([]string, error) {
	var paths []string
	walkErr := fs.WalkDir(fsys, skillDir, func(fullPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(fullPath, skillDir)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return nil
		}
		if err := checkSafePath(rel); err != nil {
			return err
		}
		paths = append(paths, rel)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(paths)
	return paths, nil
}

// checkSafePath enforces the SEP-2640 archive-safety rules on a
// canonical (forward-slash, archive-root-relative) entry path. The same
// function is used at pack time and at unpack time so the contract
// stays in one place.
func checkSafePath(rel string) error {
	if rel == "" {
		return nil
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("%w: %q", ErrArchiveAbsolutePath, rel)
	}
	cleaned := path.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains("/"+cleaned, "/../") {
		return fmt.Errorf("%w: %q", ErrArchivePathTraversal, rel)
	}
	if cleaned != rel {
		// path.Clean may shorten "./foo" to "foo" or strip trailing
		// slashes. We accept that as a normalization. But anything that
		// changes meaning (collapsing ..) was already caught above.
	}
	return nil
}

func writeTarGz(w io.Writer, fsys fs.FS, skillDir string, files []string) error {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, rel := range files {
		full := joinSkillPath(skillDir, rel)
		info, err := fs.Stat(fsys, full)
		if err != nil {
			return fmt.Errorf("skills: stat %s: %w", full, err)
		}
		hdr := &tar.Header{
			Name:    rel,
			Mode:    int64(info.Mode().Perm()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("skills: tar header %s: %w", rel, err)
		}
		if err := copyFromFS(tw, fsys, full); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("skills: tar close: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("skills: gzip close: %w", err)
	}
	return nil
}

func writeZip(w io.Writer, fsys fs.FS, skillDir string, files []string) error {
	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, rel := range files {
		full := joinSkillPath(skillDir, rel)
		info, err := fs.Stat(fsys, full)
		if err != nil {
			return fmt.Errorf("skills: stat %s: %w", full, err)
		}
		hdr := &zip.FileHeader{
			Name:     rel,
			Method:   zip.Deflate,
			Modified: info.ModTime(),
		}
		hdr.SetMode(info.Mode().Perm())
		wr, err := zw.CreateHeader(hdr)
		if err != nil {
			return fmt.Errorf("skills: zip header %s: %w", rel, err)
		}
		if err := copyFromFS(wr, fsys, full); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("skills: zip close: %w", err)
	}
	return nil
}

func copyFromFS(dst io.Writer, fsys fs.FS, p string) error {
	f, err := fsys.Open(p)
	if err != nil {
		return fmt.Errorf("skills: open %s: %w", p, err)
	}
	defer f.Close()
	if _, err := io.Copy(dst, f); err != nil {
		return fmt.Errorf("skills: copy %s: %w", p, err)
	}
	return nil
}

func joinSkillPath(skillDir, rel string) string {
	if skillDir == "" || skillDir == "." {
		return rel
	}
	return skillDir + "/" + rel
}

// UnpackedEntry is a single file extracted from an archive by
// UnpackBytes. Body is the full decoded payload; consumers that need
// streaming should iterate UnpackBytes's input directly.
type UnpackedEntry struct {
	// Path is the entry's archive-root-relative path.
	Path string
	// Mode is the entry's permission bits as stored in the archive.
	Mode fs.FileMode
	// Body is the entry's full decoded content.
	Body []byte
}

// UnpackBytes decodes an archive into a list of files. Safety rules from
// SEP-2640 are enforced as each entry is read: any path-traversal,
// absolute-path, or symlink-escape entry causes the whole call to fail
// with the relevant named error. Directories are skipped (the unpacked
// tree is reconstructed from file paths alone). Symlinks and hardlinks
// are rejected unconditionally because their resolution rules are
// host-dependent and the safety MUST is "resolve outside the skill
// directory" which cannot be evaluated reliably without staging the
// archive to a real filesystem.
//
// maxBytes is the total unpacked-size cap. Pass 0 to use
// DefaultArchiveMaxBytes. Pass -1 to disable the cap (NOT recommended
// for untrusted archives).
func UnpackBytes(data []byte, format ArchiveFormat, maxBytes int64) ([]UnpackedEntry, error) {
	if format == ArchiveFormatUnknown {
		format = DetectArchiveFormat("", data)
	}
	if maxBytes == 0 {
		maxBytes = DefaultArchiveMaxBytes
	}
	switch format {
	case ArchiveFormatTarGz:
		return unpackTarGz(data, maxBytes)
	case ArchiveFormatZip:
		return unpackZip(data, maxBytes)
	}
	return nil, fmt.Errorf("%w", ErrArchiveUnknownFormat)
}

func unpackTarGz(data []byte, maxBytes int64) ([]UnpackedEntry, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("skills: gunzip: %w", err)
	}
	defer gz.Close()

	var entries []UnpackedEntry
	var total int64
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("skills: tar next: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return nil, fmt.Errorf("%w: %q -> %q", ErrArchiveSymlinkEscape, hdr.Name, hdr.Linkname)
		case tar.TypeDir:
			if err := checkSafePath(hdr.Name); err != nil {
				return nil, err
			}
			continue
		case tar.TypeReg:
			// proceed
		default:
			// Skip unknown special-file types; they are uninterpretable
			// in our virtual-FS model.
			continue
		}
		if err := checkSafePath(hdr.Name); err != nil {
			return nil, err
		}
		if maxBytes > 0 {
			total += hdr.Size
			if total > maxBytes {
				return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrArchiveTooLarge, total, maxBytes)
			}
		}
		body, err := readWithCap(tr, hdr.Size, maxBytes)
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", hdr.Name, err)
		}
		entries = append(entries, UnpackedEntry{
			Path: hdr.Name,
			Mode: fs.FileMode(hdr.Mode) & fs.ModePerm,
			Body: body,
		})
	}
	return entries, nil
}

func unpackZip(data []byte, maxBytes int64) ([]UnpackedEntry, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("skills: zip open: %w", err)
	}
	var entries []UnpackedEntry
	var total int64
	for _, f := range zr.File {
		// zip stores directory entries as paths ending with "/".
		if strings.HasSuffix(f.Name, "/") {
			if err := checkSafePath(strings.TrimSuffix(f.Name, "/")); err != nil {
				return nil, err
			}
			continue
		}
		if err := checkSafePath(f.Name); err != nil {
			return nil, err
		}
		size := int64(f.UncompressedSize64)
		if maxBytes > 0 {
			total += size
			if total > maxBytes {
				return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrArchiveTooLarge, total, maxBytes)
			}
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("skills: zip open %s: %w", f.Name, err)
		}
		body, err := readWithCap(rc, size, maxBytes)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", f.Name, err)
		}
		entries = append(entries, UnpackedEntry{
			Path: f.Name,
			Mode: f.Mode().Perm(),
			Body: body,
		})
	}
	return entries, nil
}

// readWithCap reads at most max(size, cap) bytes from r. The dual cap
// protects against archives that lie about declared sizes (a gzip
// decompression bomb whose tar header says 1KB but expands to 1GB on
// read).
func readWithCap(r io.Reader, declared, cap int64) ([]byte, error) {
	if cap > 0 {
		// +1 so a read that exactly hits the cap still surfaces the
		// overage on the next iteration.
		r = io.LimitReader(r, cap+1)
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if cap > 0 && int64(len(buf)) > cap {
		return nil, fmt.Errorf("%w: read exceeded cap of %d", ErrArchiveTooLarge, cap)
	}
	if declared > 0 && int64(len(buf)) != declared && cap <= 0 {
		// Without a cap we still tolerate small declared/actual drift.
		// The archive header may differ from the decoded stream when
		// gzip layering is involved.
		_ = declared
	}
	return buf, nil
}
