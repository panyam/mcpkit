package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/panyam/mcpkit/server"
)

// Indexer computes the SEP-2640 discovery index for a Provider's skills,
// digests each artifact with SHA-256, and exposes the result via
// resources/read of skill://index.json.
//
// The zero value is not useful. Call NewIndexer.
//
// Indexer is safe for concurrent use; Index() takes the cache lock
// internally. The cache invalidates on the first of two events:
//
//   - the configured cache TTL elapses, or
//   - any cataloged skill's SKILL.md mtime differs from its mtime at
//     cache-build time.
//
// When the underlying fs.FS reports a zero ModTime (notably embed.FS),
// mtime invalidation cannot run for that build and the cache reverts to
// TTL-only freshness. With TTL also unset (zero), every Index() call
// recomputes.
//
// WithMtimeChecks(false) disables the per-skill mtime comparison for a
// backing where fs.Stat is expensive (an S3/HTTP-rooted fs.FS): the cache
// then invalidates on TTL and explicit Provider.NotifyChanged only. See that
// option for the tradeoff.
type Indexer struct {
	provider *Provider
	cfg      indexerConfig

	mu     sync.Mutex
	cached *cacheEntry
}

type indexerConfig struct {
	ttl time.Duration
	// mtimeChecks enables the per-skill fs.Stat mtime-comparison branch of
	// isFresh. Default true; WithMtimeChecks(false) disables it for
	// backings where fs.Stat is expensive (issue 576).
	mtimeChecks bool
	// listTTLMs and listCacheScope are the SEP-2549 attributes attached to
	// skills/list results. Zero / empty omit the attribute; see
	// WithListCacheHints for why there is no default.
	listTTLMs      int
	listCacheScope string
}

type cacheEntry struct {
	entries []SkillEntry
	builtAt time.Time
	mtimes  map[string]time.Time // skill dir path → SKILL.md mtime at build time
	version uint64               // Provider.Version() at the time this entry was built
	// noMtime is true when any skill's SKILL.md reported zero ModTime at
	// build time. While noMtime is true the cache falls back to TTL-only
	// invalidation; mtime comparison is skipped.
	noMtime bool
}

// IndexerOption configures an Indexer via NewIndexer.
type IndexerOption func(*indexerConfig)

// WithIndexerCacheTTL sets the duration the indexer caches a computed
// Index before recomputing. The default (zero) means every Index() call
// recomputes. Mtime-based invalidation runs in addition to TTL on
// fs.FS implementations that report a non-zero ModTime.
func WithIndexerCacheTTL(d time.Duration) IndexerOption {
	return func(c *indexerConfig) {
		c.ttl = d
	}
}

// WithMtimeChecks toggles the per-skill mtime-comparison branch of cache
// invalidation. Default true: on every cache hit the Indexer fs.Stat's each
// cataloged skill and recomputes when any mtime moved — cheap on local disk,
// correct for edit-in-place workflows.
//
// Pass false for a backing where fs.Stat is expensive (an S3/HTTP-rooted
// fs.FS, etc.), where the per-skill stat round-trip dominates the cache-hit
// cost and defeats the point of caching. The cache then invalidates on TTL
// (WithIndexerCacheTTL) and explicit Provider.NotifyChanged only. With mtime
// checks off AND a zero TTL there is nothing to drive invalidation, so
// Index() recomputes every call (the same fallback as a zero-ModTime fs.FS) —
// pair WithMtimeChecks(false) with a non-zero TTL.
func WithMtimeChecks(enabled bool) IndexerOption {
	return func(c *indexerConfig) {
		c.mtimeChecks = enabled
	}
}

// WithListCacheHints overrides the SEP-2549 list-caching attributes carried
// on skills/list results, which SEP-2640 expects on protocol 2026-07-28 and
// later.
//
// The default scope is "public", which is accurate for this Indexer: a
// Provider draws from one fs.FS fixed at construction, so every caller gets
// the same catalog and a shared cache can serve it to all of them. A Provider
// that ever filters by principal must pass a narrower scope here, because
// "public" would then let an intermediary hand one caller's listing to
// another.
//
// The default ttlMs is the Indexer's own cache TTL when one is configured,
// and omitted otherwise, so clients are never told to cache a listing for
// longer than the server itself considers it fresh.
//
// ttlMs <= 0 or an empty scope omits that attribute.
func WithListCacheHints(ttlMs int, cacheScope string) IndexerOption {
	return func(c *indexerConfig) {
		c.listTTLMs = ttlMs
		c.listCacheScope = cacheScope
	}
}

// NewIndexer constructs an Indexer that draws skills from provider.
// The provider must already be populated (NewProvider returned without
// error); subsequent changes to the provider's catalog are not picked
// up here. Live mutation is the concern of ext/skills issue 564
// (hot-reload).
func NewIndexer(provider *Provider, opts ...IndexerOption) *Indexer {
	idx := &Indexer{provider: provider, cfg: indexerConfig{
		mtimeChecks:    true,
		listCacheScope: CacheScopePublic,
	}}
	for _, opt := range opts {
		opt(&idx.cfg)
	}
	return idx
}

// Invalidate marks the cached index entry stale so the next Index()
// call rebuilds. Provider.NotifyChanged calls this when the version
// counter bumps; tests can use it to drive cache regeneration
// deterministically without waiting for TTL or mtime changes.
//
// Safe to call before any cache has been built (no-op) and from any
// goroutine.
func (i *Indexer) Invalidate() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cached = nil
}

func (i *Indexer) isFresh() bool {
	if i.cached == nil {
		return false
	}
	// Version is the explicit invalidation signal — any NotifyChanged
	// call bumps it past the cached value, so a mismatch trumps the
	// TTL / mtime paths below. The two slower checks remain as fallback
	// invalidation drivers for adopters who never call NotifyChanged.
	if i.provider.Version() != i.cached.version {
		return false
	}
	if i.cfg.ttl > 0 && time.Since(i.cached.builtAt) > i.cfg.ttl {
		return false
	}
	// Mtime comparison is skipped when disabled by option (issue 576) or when
	// the backing fs.FS reported no ModTime at build time. Either way the
	// cache is TTL-only: fresh until the TTL elapses, or — with a zero TTL —
	// recomputed every call, since nothing else drives invalidation.
	if !i.cfg.mtimeChecks || i.cached.noMtime {
		return i.cfg.ttl > 0
	}
	for _, skill := range i.provider.skills {
		want, ok := i.cached.mtimes[skill.dirPath]
		if !ok {
			return false
		}
		got, err := i.skillMtime(skill)
		if err != nil {
			return false
		}
		if !got.Equal(want) {
			return false
		}
	}
	return true
}

// skillMtime returns the mtime the cache uses to drive invalidation for
// the given skill. In file mode the cache tracks SKILL.md's mtime; in
// archive mode it tracks the subtree max mtime because the digest is over
// the packed archive.
//
// Note (issue 866): file mode now also pins supporting-file digests in
// the entry's supporting-file pins, which the SKILL.md mtime does not cover. A supporting
// file that changes without a SKILL.md touch is therefore refreshed on
// the TTL boundary (or via the push-based NotifyChanged path, issue 795),
// not the instant its mtime moves — the same best-effort staleness window
// SKILL.md content already had. Walking the whole subtree on every cache
// hit to close that window would defeat the cache; it is intentionally
// out of scope here.
func (i *Indexer) skillMtime(skill *skillEntry) (time.Time, error) {
	if i.provider.cfg.archiveMode != ArchiveFormatUnknown {
		return subtreeMaxMtime(i.provider.cfg.fsys, skill.dirPath)
	}
	return mtimeOf(i.provider.cfg.fsys, manifestPath(skill.dirPath))
}

func subtreeMaxMtime(fsys fs.FS, root string) (time.Time, error) {
	var latest time.Time
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := fs.Stat(fsys, p)
		if err != nil {
			return err
		}
		mt := info.ModTime()
		if mt.After(latest) {
			latest = mt
		}
		return nil
	})
	return latest, err
}

// RegisterWith installs the SEP-2640 skills/list and skills/get methods onto
// srv. Both handlers call Entries() at request time so results reflect cache
// state plus any invalidation since the last call.
//
// Both are registered together because declaring the extension commits a
// server to both. A server with nothing to enumerate returns an empty
// skills/list rather than declining the method.
func (i *Indexer) RegisterWith(srv *server.Server) {
	srv.HandleMethod(MethodSkillsList, i.handleSkillsList)
	srv.HandleMethod(MethodSkillsGet, i.handleSkillsGet)
	i.warnOnLimits()
}

// Entries returns the SEP-2640 skill entries this server publishes, one per
// skill, each carrying its verbatim frontmatter and complete resource
// manifest.
//
// Shares the Index cache: entries are rebuilt on the same freshness rules
// (version counter, TTL, mtime) documented on Indexer, so a skills/list and a
// concurrent index read observe the same snapshot.
//
// Callers should treat the result as immutable; successive calls may return
// the same backing array.
func (i *Indexer) Entries() ([]SkillEntry, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.isFresh() {
		return i.cached.entries, nil
	}

	version := i.provider.Version()
	entries, mtimes, noMtime, err := i.buildEntries()
	if err != nil {
		return nil, err
	}
	i.cached = &cacheEntry{
		entries: entries,
		builtAt: time.Now(),
		mtimes:  mtimes,
		version: version,
		noMtime: noMtime,
	}
	return entries, nil
}

// buildEntries walks every registered skill and produces its SkillEntry,
// alongside the per-skill mtimes the cache compares on the next call.
//
// noMtime reports that at least one skill's SKILL.md returned a zero ModTime
// (notably embed.FS). The cache falls back to TTL-only freshness for that
// build, since mtime comparison cannot distinguish anything then.
//
// Sorted by URI so a listing is stable across calls, which cursor-based
// pagination depends on.
func (i *Indexer) buildEntries() ([]SkillEntry, map[string]time.Time, bool, error) {
	out := make([]SkillEntry, 0, len(i.provider.skills))
	mtimes := make(map[string]time.Time, len(i.provider.skills))
	var anyZeroMtime bool

	for _, skill := range i.provider.skills {
		resources, err := i.buildResources(skill)
		if err != nil {
			return nil, nil, false, err
		}
		out = append(out, SkillEntry{
			URI:         skillManifestURI(skill.uriSegs),
			Frontmatter: frontmatterJSON(skill.fm),
			Resources:   resources,
		})

		mtime, err := i.skillMtime(skill)
		if err != nil {
			return nil, nil, false, fmt.Errorf("skills: mtime %s: %w", skill.dirPath, err)
		}
		if mtime.IsZero() {
			anyZeroMtime = true
		}
		mtimes[skill.dirPath] = mtime
	}

	sort.Slice(out, func(a, b int) bool { return out[a].URI < out[b].URI })
	return out, mtimes, anyZeroMtime, nil
}

// digestOf computes the SEP-2640 digest format over the raw artifact
// bytes: "sha256:" followed by 64 lowercase hex characters.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// mtimeOf returns the mtime of path within fsys, or the zero time
// when the underlying fs.FS does not expose timing information.
func mtimeOf(fsys fs.FS, p string) (time.Time, error) {
	info, err := fs.Stat(fsys, p)
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return time.Time{}, fmt.Errorf("%w", err)
		}
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func manifestPath(skillDir string) string {
	return skillDir + "/" + ManifestFilename
}

func skillManifestURI(uriSegs []string) string {
	return Scheme + "://" + joinSegments(uriSegs) + "/" + ManifestFilename
}

func joinSegments(segs []string) string {
	switch len(segs) {
	case 0:
		return ""
	case 1:
		return segs[0]
	}
	out := segs[0]
	for _, s := range segs[1:] {
		out = out + "/" + s
	}
	return out
}
