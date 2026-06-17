package skills

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/ext/skills/fsutil"
)

// SourceFS is the return type for every source adapter — a read-only
// fs.FS that is also an io.Closer. Most adapters need cleanup (file
// handles, tempfiles), so callers MUST defer Close. The interface is
// declared explicitly so adapters can satisfy it without leaking
// implementation types.
type SourceFS interface {
	fs.FS
	io.Closer
}

// noopCloser wraps an fs.FS that owns no resources, so it can satisfy
// SourceFS without forcing callers to know whether Close is meaningful.
type noopCloser struct{ fs.FS }

func (noopCloser) Close() error { return nil }

// OpenArchive opens a local archive file and returns a SourceFS view.
// Format is detected from the file suffix or magic bytes.
//
// For ZIP archives the returned SourceFS is disk-backed via
// fsutil.ZipFS — only the central directory is mapped; per-file reads
// stream from disk on demand. For tar.gz / tar.bz2 the archive is
// loaded fully into memory via NewArchiveFS (tar is a streaming
// format with no random access). Callers SHOULD defer Close.
//
// # Auto-wrap by frontmatter name
//
// When the archive contains a SKILL.md at its root (as produced by
// PackSkill), OpenArchive parses that file's frontmatter and wraps
// the returned SourceFS under a top-level directory matching the
// frontmatter name. So a tar.gz built by
// PackSkill(fsys, "git-workflow", TarGz) — whose root holds
// SKILL.md + supporting files with frontmatter name="git-workflow" —
// presents as if its layout were git-workflow/SKILL.md + supporting
// files. This is required for SEP-2640 compliance when the archive
// is mounted under any prefix: Provider's rule is path.Base(skillDir)
// == frontmatter.Name, so the skill-name directory must exist in the
// served path.
//
// Multi-skill archives — those without a root-level SKILL.md but
// containing one or more <skill-name>/SKILL.md subdirectories — are
// served as-is. Auto-wrap is a no-op in that case.
//
// Errors:
//   - os.PathError on read failure.
//   - ErrArchiveUnknownFormat when the file's format cannot be
//     identified from suffix or magic bytes.
//   - Frontmatter parse errors when the root SKILL.md is malformed.
func OpenArchive(path string) (SourceFS, error) {
	raw, err := openArchiveRaw(path)
	if err != nil {
		return nil, err
	}
	return autoWrapByFrontmatter(raw, path)
}

func openArchiveRaw(path string) (SourceFS, error) {
	format := DetectArchiveFormat(path, nil)
	if format == ArchiveFormatUnknown {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		peek := make([]byte, 8)
		n, _ := io.ReadFull(f, peek)
		f.Close()
		format = DetectArchiveFormat(path, peek[:n])
		if format == ArchiveFormatUnknown {
			return nil, fmt.Errorf("%w: %q", ErrArchiveUnknownFormat, path)
		}
	}
	if format == ArchiveFormatZip {
		return fsutil.OpenZipFS(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	afs, err := NewArchiveFS(data, format, 0)
	if err != nil {
		return nil, err
	}
	return noopCloser{FS: afs}, nil
}

// autoWrapByFrontmatter wraps raw under a top-level directory matching
// the archive's root SKILL.md frontmatter name, so the archive's
// post-wrap shape always satisfies SEP-2640's Provider.walk rule
// (path.Base(skillDir) == frontmatter.Name).
//
// Behavior:
//   - Archive has root-level SKILL.md (PackSkill output) → wraps the
//     fs.FS under "<frontmatter.name>/" so the canonical SEP path
//     shape works under any prefix.
//   - Archive has no root-level SKILL.md (multi-skill catalog, or
//     non-skill archive) → returns raw unchanged. Provider walks it
//     directly and finds each <skill-name>/SKILL.md.
//   - Root SKILL.md exists but its frontmatter is malformed →
//     returns a wrapped frontmatter parse error so the caller knows
//     the archive is unusable.
//
// Source is the path string used in error messages; pass the URL or
// path the archive was loaded from.
func autoWrapByFrontmatter(raw SourceFS, source string) (SourceFS, error) {
	skillBytes, err := fs.ReadFile(raw, "SKILL.md")
	if err != nil {
		// No root SKILL.md — leave raw alone. fs.ReadFile returns the
		// fs-typed error; ignore (not-found or non-dir) — treat as
		// "this isn't a single-skill archive."
		return raw, nil
	}
	fm, _, err := ParseFrontmatter(skillBytes)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("skills: archive %q has invalid root SKILL.md: %w", source, err)
	}
	if fm.Name == "" {
		raw.Close()
		return nil, fmt.Errorf("skills: archive %q root SKILL.md missing frontmatter name", source)
	}
	wrapped, err := fsutil.NewLayered(fsutil.Layer{Prefix: fm.Name, FSys: raw, Closer: raw})
	if err != nil {
		raw.Close()
		return nil, err
	}
	return wrapped, nil
}

// OpenArchivesDir reads every archive file in dir (matching
// .tar.gz / .zip / .tar.bz2 suffixes) and returns a SourceFS that
// flat-merges their contents at the root. Each archive's auto-wrap
// (via OpenArchive) ensures single-skill archives surface as
// <frontmatter-name>/SKILL.md; multi-skill catalog archives
// contribute their already-named subdirectories directly.
//
// Result:
//   - PackSkill-style archive "git-workflow.tar.gz" (root SKILL.md,
//     frontmatter name="git-workflow") contributes "git-workflow/"
//     at the merged root.
//   - Catalog archive "bundle.tar.gz" (containing "git-workflow/",
//     "pdf-processing/" at its root) contributes both directly.
//
// Two archives contributing the same root-level entry name error at
// construction (via fsutil.NewFlat's collision check) — operators
// see ambiguity immediately rather than silently losing data.
//
// Non-archive files in dir are skipped. Subdirectories are not
// recursed into; for nested layouts compose multiple OpenArchivesDir
// calls via fsutil.NewLayered with explicit prefixes.
func OpenArchivesDir(dir string) (SourceFS, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("skills: read archives dir %q: %w", dir, err)
	}
	var layers []fsutil.Layer
	cleanup := func() {
		for _, l := range layers {
			if l.Closer != nil {
				l.Closer.Close()
			}
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		format := DetectArchiveFormat(name, nil)
		if format == ArchiveFormatUnknown {
			continue
		}
		path := filepath.Join(dir, name)
		inner, err := OpenArchive(path)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("skills: open archive %q in dir: %w", path, err)
		}
		layers = append(layers, fsutil.Layer{FSys: inner, Closer: inner})
	}
	if len(layers) == 0 {
		return nil, fmt.Errorf("skills: archives dir %q has no recognized archives", dir)
	}
	merged, err := fsutil.NewFlat(layers...)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: %v", ErrArchivesDirCollision, err)
	}
	return merged, nil
}

// FetchOption tunes FetchArchive.
type FetchOption func(*fetchConfig)

type fetchConfig struct {
	client          *http.Client
	maxBytes        int64
	requestModifier func(*http.Request)
	streamToDir     string
	streamToDirSet  bool
}

// WithHTTPClient overrides the *http.Client used for the fetch.
// Default is http.DefaultClient. Adopters needing custom transports
// (proxy, mTLS, retries via middleware), timeouts, or redirect
// policies configure them on this client directly — there is no
// per-option wrapper, the client IS the configuration surface.
func WithHTTPClient(c *http.Client) FetchOption {
	return func(cfg *fetchConfig) {
		cfg.client = c
	}
}

// WithHTTPMaxBytes caps the response body's accepted size. Default
// is DefaultArchiveMaxBytes (100 MiB). Pass -1 to disable the cap
// entirely. When the cap is exceeded the fetch errors with
// ErrArchiveExceedsMaxBytes and any partial bytes are discarded.
func WithHTTPMaxBytes(n int64) FetchOption {
	return func(cfg *fetchConfig) {
		cfg.maxBytes = n
	}
}

// WithRequestModifier installs a callback that mutates the
// *http.Request immediately before the fetch fires. Use for per-call
// headers (User-Agent, Authorization, GitHub PAT, etc.) without
// having to build a custom RoundTripper for one-off concerns.
func WithRequestModifier(fn func(*http.Request)) FetchOption {
	return func(cfg *fetchConfig) {
		cfg.requestModifier = fn
	}
}

// WithStreamToDisk streams the response body to a temp file under
// dir, then opens it disk-backed. For ZIP archives this means the
// returned SourceFS reads per-file from disk throughout (low
// steady-state memory). For tar formats the bytes still load into
// memory after the stream completes (tar has no random access).
//
// Pass empty string to use os.TempDir(). The tempfile is removed
// when the SourceFS's Close() is called.
//
// Default: in-memory.
func WithStreamToDisk(dir string) FetchOption {
	return func(cfg *fetchConfig) {
		if dir == "" {
			dir = os.TempDir()
		}
		cfg.streamToDir = dir
		cfg.streamToDirSet = true
	}
}

// FetchArchive issues an HTTP GET against url and returns the
// resulting archive as a SourceFS. Honors ctx for cancellation,
// the configured size cap, and the optional disk-streaming path.
//
// Format detection: tries the URL suffix first, then sniffs the
// first 8 bytes of the response if the suffix is missing.
//
// Errors:
//   - ctx.Err() on cancellation.
//   - ErrArchiveDownloadFailed when the transport fails or the
//     response is non-2xx (status wrapped in the error message).
//   - ErrArchiveExceedsMaxBytes when the body exceeds the cap.
//   - ErrArchiveUnknownFormat when the bytes cannot be classified.
func FetchArchive(ctx context.Context, url string, opts ...FetchOption) (SourceFS, error) {
	cfg := fetchConfig{
		client:   http.DefaultClient,
		maxBytes: DefaultArchiveMaxBytes,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrArchiveDownloadFailed, err)
	}
	if cfg.requestModifier != nil {
		cfg.requestModifier(req)
	}
	resp, err := cfg.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrArchiveDownloadFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d %s", ErrArchiveDownloadFailed, resp.StatusCode, resp.Status)
	}

	body := io.Reader(resp.Body)
	if cfg.maxBytes > 0 {
		body = io.LimitReader(body, cfg.maxBytes+1)
	}

	if cfg.streamToDirSet {
		return fetchToDisk(cfg.streamToDir, body, cfg.maxBytes, url)
	}
	return fetchToMemory(body, cfg.maxBytes, url)
}

func fetchToMemory(body io.Reader, maxBytes int64, srcURL string) (SourceFS, error) {
	buf, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %w", ErrArchiveDownloadFailed, err)
	}
	if maxBytes > 0 && int64(len(buf)) > maxBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrArchiveExceedsMaxBytes, len(buf), maxBytes)
	}
	format := DetectArchiveFormat(srcURL, buf)
	if format == ArchiveFormatUnknown {
		return nil, fmt.Errorf("%w: from %q", ErrArchiveUnknownFormat, srcURL)
	}
	afs, err := NewArchiveFS(buf, format, 0)
	if err != nil {
		return nil, err
	}
	return noopCloser{FS: afs}, nil
}

func fetchToDisk(dir string, body io.Reader, maxBytes int64, srcURL string) (SourceFS, error) {
	f, err := os.CreateTemp(dir, "skills-fetch-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create tempfile in %q: %w", ErrArchiveDownloadFailed, dir, err)
	}
	n, err := io.Copy(f, body)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("%w: stream body: %w", ErrArchiveDownloadFailed, err)
	}
	if maxBytes > 0 && n > maxBytes {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("%w: %d > %d", ErrArchiveExceedsMaxBytes, n, maxBytes)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("%w: rewind tempfile: %w", ErrArchiveDownloadFailed, err)
	}
	// Peek for format detection.
	peek := make([]byte, 8)
	m, _ := io.ReadFull(f, peek)
	f.Seek(0, io.SeekStart)
	format := DetectArchiveFormat(srcURL, peek[:m])
	if format == ArchiveFormatUnknown {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("%w: from %q", ErrArchiveUnknownFormat, srcURL)
	}
	tempPath := f.Name()
	f.Close()
	if format == ArchiveFormatZip {
		rc, err := zip.OpenReader(tempPath)
		if err != nil {
			os.Remove(tempPath)
			return nil, fmt.Errorf("%w: open zip tempfile: %w", ErrArchiveDownloadFailed, err)
		}
		return &tempfileZipFS{ZipFS: fsutil.NewZipFS(rc), path: tempPath}, nil
	}
	// tar.gz / tar.bz2: still need full bytes for the streaming-tar
	// path. Read the tempfile back; we already paid the disk-write
	// cost so this is just shifting the memory point in time. The
	// tempfile is removed on Close.
	data, err := os.ReadFile(tempPath)
	if err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("%w: re-read tempfile: %w", ErrArchiveDownloadFailed, err)
	}
	os.Remove(tempPath)
	afs, err := NewArchiveFS(data, format, 0)
	if err != nil {
		return nil, err
	}
	return noopCloser{FS: afs}, nil
}

// tempfileZipFS pairs a disk-backed ZipFS with its tempfile so the
// tempfile is removed when Close is called.
type tempfileZipFS struct {
	*fsutil.ZipFS
	path string
}

func (t *tempfileZipFS) Close() error {
	err := t.ZipFS.Close()
	if rmErr := os.Remove(t.path); err == nil {
		err = rmErr
	}
	return err
}

// GitHubOption tunes FetchGitHubArchive.
type GitHubOption func(*githubConfig)

type githubConfig struct {
	subdir      string
	fetchOpts   []FetchOption
}

// WithGitHubSubdir re-roots the returned SourceFS into a subdirectory
// of the repo. Most adopter repos store skills under a subdir like
// "skills/" rather than at the repo root; this skips the noise.
func WithGitHubSubdir(subdir string) GitHubOption {
	return func(cfg *githubConfig) {
		cfg.subdir = subdir
	}
}

// WithGitHubFetchOptions forwards FetchOption values to the
// underlying FetchArchive call (HTTP client, size cap, stream-to-disk,
// request modifier). Pass an Authorization header here for private
// repos.
func WithGitHubFetchOptions(opts ...FetchOption) GitHubOption {
	return func(cfg *githubConfig) {
		cfg.fetchOpts = append(cfg.fetchOpts, opts...)
	}
}

// FetchGitHubArchive fetches a tarball of <owner>/<repo>@<ref> from
// GitHub's archive endpoint and returns a SourceFS rooted at the
// repository contents (the tarball's top-level <repo>-<safe-ref>
// directory is auto-detected and stripped). Optionally re-roots into
// a subdir via WithGitHubSubdir.
//
// ref accepts branches (e.g. "main"), tags ("v1.2.3"), commit SHAs,
// and ref-form strings ("refs/heads/main"). Slashes in ref are
// URL-encoded; the tarball's mangled top-level dir is discovered by
// reading the unpacked archive's root entry rather than reverse-
// engineering GitHub's encoding rules.
//
// Public repos work without auth. For private repos pass a PAT via
// WithGitHubFetchOptions(WithRequestModifier(func(r *http.Request) {
//   r.Header.Set("Authorization", "Bearer " + pat)
// })).
func FetchGitHubArchive(ctx context.Context, owner, repo, ref string, opts ...GitHubOption) (SourceFS, error) {
	cfg := githubConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	githubURL := fmt.Sprintf("https://github.com/%s/%s/archive/%s.tar.gz",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	root, err := FetchArchive(ctx, githubURL, cfg.fetchOpts...)
	if err != nil {
		return nil, err
	}
	topLevel, err := detectTopLevelDir(root)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("skills: github archive: detect top-level dir: %w", err)
	}
	sub := topLevel
	if cfg.subdir != "" {
		sub = topLevel + "/" + strings.Trim(cfg.subdir, "/")
	}
	subFS, err := fs.Sub(root, sub)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("skills: github archive: re-root to %q: %w", sub, err)
	}
	return &subSourceFS{FS: subFS, closer: root}, nil
}

// detectTopLevelDir reads the single root entry of a GitHub-archive
// fs.FS. GitHub tarballs always have one top-level directory whose
// name encodes <repo>-<safe-ref>; we don't care about the exact name,
// just that there's one.
func detectTopLevelDir(root SourceFS) (string, error) {
	rootDir, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer rootDir.Close()
	dirReader, ok := rootDir.(fs.ReadDirFile)
	if !ok {
		return "", fmt.Errorf("root is not a directory")
	}
	entries, err := dirReader.ReadDir(-1)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("archive is empty")
	}
	if len(entries) > 1 {
		// GitHub auto-archives always have one top dir. If we see more,
		// the URL probably wasn't a GitHub archive after all; surface
		// it so the caller can investigate.
		return "", fmt.Errorf("expected single top-level dir, got %d entries", len(entries))
	}
	if !entries[0].IsDir() {
		return "", fmt.Errorf("top-level entry %q is not a directory", entries[0].Name())
	}
	return entries[0].Name(), nil
}

// subSourceFS combines an fs.Sub'd FS with the closer of the parent
// archive — closing the sub closes the underlying archive.
type subSourceFS struct {
	fs.FS
	closer io.Closer
}

func (s *subSourceFS) Close() error { return s.closer.Close() }
