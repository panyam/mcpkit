package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
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
type Indexer struct {
	provider *Provider
	cfg      indexerConfig

	mu     sync.Mutex
	cached *cacheEntry
}

type indexerConfig struct {
	ttl time.Duration
}

type cacheEntry struct {
	index   Index
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

// NewIndexer constructs an Indexer that draws skills from provider.
// The provider must already be populated (NewProvider returned without
// error); subsequent changes to the provider's catalog are not picked
// up here. Live mutation is the concern of ext/skills issue 564
// (hot-reload).
func NewIndexer(provider *Provider, opts ...IndexerOption) *Indexer {
	idx := &Indexer{provider: provider}
	for _, opt := range opts {
		opt(&idx.cfg)
	}
	return idx
}

// IndexResourceDef is the ResourceDef registered for skill://index.json.
// Servers reuse this for documentation surfaces (READMEs, OpenAPI-style
// catalogs) so the wire-level name stays in lock-step with the runtime
// resource.
var IndexResourceDef = core.ResourceDef{
	URI:         IndexURI,
	Name:        "Skill discovery index",
	Description: "JSON catalog of skills served by this server per SEP-2640.",
	MimeType:    "application/json",
}

// Index returns the discovery index per SEP-2640.
//
// Each entry's Digest is a sha256:{64-hex} string over the raw bytes of
// the entry's canonical artifact. For skill-md entries that artifact is
// the SKILL.md file; archive entries (ext/skills issue 561) will hash the
// archive bytes instead.
//
// The returned Index is the cached value when cache freshness rules
// allow; otherwise it is freshly computed. Callers should treat the
// returned Index as immutable. Subsequent calls may return the same
// underlying slices.
//
// Errors propagate the underlying fs.FS read errors with the source
// path attached.
func (i *Indexer) Index() (Index, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.isFresh() {
		return i.cached.index, nil
	}

	version := i.provider.Version()
	idx, mtimes, noMtime, err := i.build()
	if err != nil {
		return Index{}, err
	}
	if idx.Meta == nil {
		idx.Meta = map[string]any{}
	}
	idx.Meta[MetaPrefix+"version"] = version
	i.cached = &cacheEntry{
		index:   idx,
		builtAt: time.Now(),
		mtimes:  mtimes,
		version: version,
		noMtime: noMtime,
	}
	return idx, nil
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
	if i.cfg.ttl == 0 && i.cached.noMtime {
		// Zero TTL plus no mtime signal means we have nothing to drive
		// invalidation off; recompute every call to avoid serving a
		// permanently stale index.
		return false
	}
	if i.cached.noMtime {
		return true
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
// the given skill. In file mode the cache invalidates on changes to
// SKILL.md only because the entry digest is over SKILL.md bytes alone.
// In archive mode the digest is over the packed archive, so any file
// in the skill's subtree contributes; the max mtime across the subtree
// is what we track.
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

func (i *Indexer) build() (Index, map[string]time.Time, bool, error) {
	entries := make([]IndexEntry, 0, len(i.provider.skills))
	mtimes := make(map[string]time.Time, len(i.provider.skills))
	var anyZeroMtime bool

	for _, skill := range i.provider.skills {
		var (
			entryType   SkillType
			entryURL    string
			digestBytes []byte
		)

		if i.provider.cfg.archiveMode != ArchiveFormatUnknown {
			packed, err := PackSkill(i.provider.cfg.fsys, skill.dirPath, i.provider.cfg.archiveMode)
			if err != nil {
				return Index{}, nil, false, fmt.Errorf("skills: pack %s for digest: %w", skill.dirPath, err)
			}
			entryType = SkillTypeArchive
			entryURL = Scheme + "://" + joinSegments(skill.uriSegs) + i.provider.cfg.archiveMode.Suffix()
			digestBytes = packed
		} else {
			manifest := manifestPath(skill.dirPath)
			raw, err := fs.ReadFile(i.provider.cfg.fsys, manifest)
			if err != nil {
				return Index{}, nil, false, fmt.Errorf("skills: read %s for digest: %w", manifest, err)
			}
			entryType = SkillTypeSkillMD
			entryURL = skillManifestURI(skill.uriSegs)
			digestBytes = raw
		}

		entries = append(entries, IndexEntry{
			Type:        entryType,
			Name:        skill.fm.Name,
			Description: skill.fm.Description,
			URL:         entryURL,
			Digest:      digestOf(digestBytes),
		})

		mtime, err := i.skillMtime(skill)
		if err != nil {
			return Index{}, nil, false, fmt.Errorf("skills: mtime %s: %w", skill.dirPath, err)
		}
		if mtime.IsZero() {
			anyZeroMtime = true
		}
		mtimes[skill.dirPath] = mtime
	}

	sortIndexEntries(entries)

	return NewIndex(entries...), mtimes, anyZeroMtime, nil
}

// RegisterWith installs the index resource onto srv. The handler calls
// Index() at request time so the result reflects cache state plus any
// invalidation since the last call.
func (i *Indexer) RegisterWith(srv *server.Server) {
	srv.RegisterResource(IndexResourceDef, i.handler())
}

func (i *Indexer) handler() core.ResourceHandler {
	return func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
		idx, err := i.Index()
		if err != nil {
			return core.ResourceResult{}, err
		}
		body, err := json.Marshal(idx)
		if err != nil {
			return core.ResourceResult{}, fmt.Errorf("skills: marshal index: %w", err)
		}
		return core.ResourceResult{
			Contents: []core.ResourceReadContent{{
				URI:      IndexURI,
				MimeType: "application/json",
				Text:     string(body),
			}},
		}, nil
	}
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

func sortIndexEntries(entries []IndexEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].URL < entries[j].URL
	})
}
