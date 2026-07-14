package skills

import (
	"io/fs"
	"os"
	"time"
)

// ProviderOption configures a Provider via NewProvider.
type ProviderOption func(*providerConfig)

type providerConfig struct {
	fsys                 fs.FS
	root                 string
	hostRoot             string // host filesystem path; populated by WithDirectory, empty for WithFS
	uriPrefix            []string
	metaPrefix           string
	suppressIndex        bool
	suppressDirectoryRead bool
	indexCacheTTL        time.Duration
	archiveMode          ArchiveFormat
	archiveMaxBytes      int64
	coalesceWindow       time.Duration
	minBroadcastInterval time.Duration
	fsWatcherEnabled     bool
	fsWatcherIgnore      []string
	fsWatcherErrHandler  func(error)
	supportingDigests    SupportingDigestMode
}

// SupportingDigestMode selects how a Provider pins the integrity of a
// skill's supporting files (every regular file under the skill directory
// except SKILL.md) in its index. The supporting-file digest shape is
// still undecided in the SEP (issues 780 / 839), so mcpkit makes the
// strategy selectable rather than committing to one on the wire.
type SupportingDigestMode int

const (
	// SupportingDigestsPerFile pins each supporting file with its own
	// SHA-256, carried under MetaKeyFileDigests in the entry's _meta. A
	// host verifies a single file on read via Client.ReadSkillFileVerified
	// without fetching the whole skill. This is the default and closes WG
	// threat B1 (issue 866).
	SupportingDigestsPerFile SupportingDigestMode = iota

	// SupportingDigestsOff pins SKILL.md only (via IndexEntry.Digest),
	// matching today's spec text exactly. Supporting files carry no pin, so
	// ReadSkillFileVerified returns ErrSupportingFileUnpinned for them. Use
	// this for strict spec-only index output until the SEP clarifies.
	SupportingDigestsOff
)

// WithSupportingFileDigests selects the supporting-file integrity strategy
// (see SupportingDigestMode). Default is SupportingDigestsPerFile.
//
// The pins live under a reverse-domain _meta key, so they never collide
// with a top-level field the SEP may later define; when the SEP settles on
// a shape mcpkit maps to it (issue 780). Until then this option lets an
// operator emit strict spec-only output (SupportingDigestsOff) or keep the
// per-file hardening (the default).
func WithSupportingFileDigests(mode SupportingDigestMode) ProviderOption {
	return func(c *providerConfig) { c.supportingDigests = mode }
}

// WithFS supplies the io/fs.FS that the Provider walks for skills. The
// FS is rooted at "." within itself. Use any fs.FS implementation:
// os.DirFS for a local directory, embed.FS for a binary-embedded tree,
// fstest.MapFS for tests, or a chained adapter that synthesizes content
// (e.g., generating SKILL.md for non-conforming directories).
func WithFS(fsys fs.FS) ProviderOption {
	return func(c *providerConfig) {
		c.fsys = fsys
		c.root = "."
	}
}

// WithDirectory is sugar for WithFS(os.DirFS(path)). It exists for the
// common local-directory case so callers do not have to spell out the
// os.DirFS wrap.
//
// Additionally captures path as providerConfig.hostRoot so WithFSWatcher
// can locate the underlying filesystem for fsnotify. Relative paths are
// fine — the watcher resolves to absolute at startup. Callers using
// WithFS instead must arrange their own change-detection (the watcher
// has no fs.FS-only path; non-os filesystems like embed.FS aren't
// watchable by construction).
func WithDirectory(path string) ProviderOption {
	return func(c *providerConfig) {
		c.fsys = os.DirFS(path)
		c.root = "."
		c.hostRoot = path
	}
}

// WithURIPrefix sets the organizational prefix that segments every
// skill's URI under. Per SEP-2640, servers MAY organize skills
// hierarchically. With prefix "acme/billing" a skill named "refunds"
// becomes skill://acme/billing/refunds/SKILL.md instead of
// skill://refunds/SKILL.md.
//
// The prefix is split on "/". An empty prefix means no prefix.
func WithURIPrefix(prefix string) ProviderOption {
	return func(c *providerConfig) {
		c.uriPrefix = splitNonEmpty(prefix, "/")
	}
}

// WithMetaPrefix overrides the reverse-domain prefix used to surface
// extra SKILL.md frontmatter fields through a resource's annotations.
// SEP-2640 recommends "io.modelcontextprotocol.skills/" and that is the
// default when this option is not supplied.
func WithMetaPrefix(prefix string) ProviderOption {
	return func(c *providerConfig) {
		c.metaPrefix = prefix
	}
}

// WithoutIndex suppresses the auto-registration of skill://index.json
// when the Provider's RegisterWith is called. Use this when the server
// wants to expose individual skill files but not the discovery index
// (e.g., a generated catalog the SEP says hosts MUST NOT treat absence
// as proof of "no skills"), or when the caller wants to construct and
// register an Indexer explicitly with non-default options.
func WithoutIndex() ProviderOption {
	return func(c *providerConfig) {
		c.suppressIndex = true
	}
}

// WithIndexCacheTTL forwards a cache TTL to the Indexer that
// Provider.RegisterWith builds for skill://index.json. Equivalent to
// constructing an Indexer explicitly with WithIndexerCacheTTL(d).
// Ignored when WithoutIndex is also supplied. See Indexer for the full
// cache semantics including the zero-mtime fallback.
func WithIndexCacheTTL(d time.Duration) ProviderOption {
	return func(c *providerConfig) {
		c.indexCacheTTL = d
	}
}

// WithArchiveMode publishes every skill as a single archive resource at
// skill://<path><suffix> instead of registering each file individually.
// Per SEP-2640, archive mode is a server-side packaging optimization
// that delivers a multi-file skill atomically in one round trip without
// changing the post-unpack virtual namespace hosts observe.
//
// Index entries for archive-mode skills carry Type:archive, URL ending
// in the format suffix, and a Digest computed over the archive bytes.
//
// Archive mode is per-Provider in this revision. Per-skill mode
// (mixing archive-served and file-served skills under one Provider) is
// deliberately out of scope; file a follow-up if a use case surfaces.
func WithArchiveMode(format ArchiveFormat) ProviderOption {
	return func(c *providerConfig) {
		c.archiveMode = format
	}
}

// WithArchiveMaxBytes caps the unpacked size of any archive the Provider
// produces or that an associated ArchiveFS reads. Pass 0 to use
// DefaultArchiveMaxBytes (100 MiB), pass -1 to disable the cap
// entirely (NOT recommended for untrusted archives).
func WithArchiveMaxBytes(n int64) ProviderOption {
	return func(c *providerConfig) {
		c.archiveMaxBytes = n
	}
}

// WithCoalesceWindow groups NotifyChanged calls that land within d of
// each other into a single version bump + broadcast. Trailing-edge: the
// timer resets on each call, so a sustained burst defers the flush
// until the burst settles. Set to 0 (default) to disable coalescing —
// every NotifyChanged call flushes immediately (subject to throttle).
//
// Within the window, paths are accumulated into a deduplicated set;
// five NotifyChanged calls naming the same path produce one entry, one
// version bump, and one broadcast. The set is reserved for the
// per-path dependency DAG that lands with #796 (sub-indexes) and #798
// (pack cache); today it serves only as the dedup key.
//
// Recommended: 100ms–500ms for fsnotify-style Detectors (editor saves
// typically fire 3–5 events in <50ms); 0 for explicit-call Detectors
// like admin endpoints where each call is intentional and unique.
func WithCoalesceWindow(d time.Duration) ProviderOption {
	return func(c *providerConfig) {
		c.coalesceWindow = d
	}
}

// WithMinBroadcastInterval enforces a minimum gap between consecutive
// broadcasts. A flush arriving within d of the last broadcast queues a
// single trailing broadcast at last+d. Set to 0 (default) to disable
// throttling.
//
// Composes with WithCoalesceWindow: coalesce runs first (group events
// into one broadcast intent); throttle enforces the minimum-interval
// contract on the actual broadcast. The version counter and the index
// cache invalidation still fire on the coalesce boundary so polling
// stateless clients see changes promptly — only the stateful-wire
// notification rate is throttled.
func WithMinBroadcastInterval(d time.Duration) ProviderOption {
	return func(c *providerConfig) {
		c.minBroadcastInterval = d
	}
}

// WatcherOption tunes the fsnotify-driven Detector that WithFSWatcher
// installs. Options are applied to providerConfig at NewProvider time.
type WatcherOption func(*providerConfig)

// WithFSWatcher enables an fsnotify-driven Detector that watches the
// Provider's hostRoot (populated by WithDirectory) and feeds change
// events into Provider.NotifyChangedEvents.
//
// Requires WithDirectory — fs.FS implementations like embed.FS and
// skills.ArchiveFS are read-only by construction and have no host
// path to watch; NewProvider returns ErrFSWatcherMissingHostRoot when
// WithFSWatcher is set against such a Provider.
//
// Lifecycle:
//   - NewProvider creates the fsnotify.Watcher and walks the host
//     directory tree to register watches. Errors on individual
//     watcher.Add calls (permission denied on a subdir, etc.) are
//     surfaced via the error handler set by WithFSWatcherErrorHandler
//     and skipped — one unreadable subdir does not fail construction.
//   - RegisterWith starts the dispatch goroutine that reads
//     watcher.Events / watcher.Errors and forwards into the Applier.
//   - Close stops the goroutine abruptly.
//   - Shutdown(ctx) drains buffered events through one final flush
//     before stopping (graceful path).
//
// Recursive watching is handled by re-walking on directory-create
// events; directory removals prune the watch set. Ignore patterns
// from WithFSWatcherIgnore are applied to both the initial walk and
// runtime additions.
//
// The Detector emits no events for the initial catalog — the state
// at construction IS the baseline. Bursts of fsnotify events (editor
// saves typically fire 3-5 events in <50ms) collapse naturally
// through the Applier's WithCoalesceWindow.
func WithFSWatcher(opts ...WatcherOption) ProviderOption {
	return func(c *providerConfig) {
		c.fsWatcherEnabled = true
		for _, opt := range opts {
			opt(c)
		}
	}
}

// WithFSWatcherIgnore appends path-fragment patterns to skip during
// the fsnotify Detector's directory walk. Any directory whose path
// (relative to hostRoot) contains one of the patterns as a path
// segment is not watched, and events for files inside it are
// suppressed.
//
// Default ignore set (always applied): ".git", "node_modules",
// ".DS_Store". Custom patterns supplement the defaults; pass an empty
// slice to keep only the defaults.
//
// Examples that match: ".git", "node_modules", "vendor".
// Examples that don't: "*.tmp" (no glob support today; file a follow-up
// if needed).
func WithFSWatcherIgnore(patterns ...string) WatcherOption {
	return func(c *providerConfig) {
		c.fsWatcherIgnore = append(c.fsWatcherIgnore, patterns...)
	}
}

// WithFSWatcherErrorHandler routes runtime fsnotify errors (buffer
// overflows on Linux, channel-level errors, per-subdir watcher.Add
// failures during the initial walk) to fn. fn is called from the
// watcher goroutine; implementations MUST NOT block (use a buffered
// channel + drain goroutine if heavy logging is required).
//
// Default is nil: errors are silently dropped. Production deployments
// SHOULD wire this to their logger / metrics surface.
func WithFSWatcherErrorHandler(fn func(error)) WatcherOption {
	return func(c *providerConfig) {
		c.fsWatcherErrHandler = fn
	}
}

// WithoutDirectoryRead suppresses registration of the SEP-2640
// resources/directory/read method when the Provider's RegisterWith is
// called. The default is ON because a Provider can always enumerate
// directories from its underlying fs.FS at trivial cost.
//
// Suppress when the caller wants the discovery index without the
// directory-navigation surface (e.g., to stay on the pre-2e04c48d SEP
// shape during a transition window, or to gate the capability behind a
// feature flag the application owns).
func WithoutDirectoryRead() ProviderOption {
	return func(c *providerConfig) {
		c.suppressDirectoryRead = true
	}
}
