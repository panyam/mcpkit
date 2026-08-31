package skills_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

func TestIndexer_Entries_DigestFormat(t *testing.T) {
	p := mustProvider(t, "testdata/valid")
	entries, err := skills.NewIndexer(p).Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected populated entries")
	}
	re := regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	for _, e := range entries {
		for _, f := range e.Resources.Files {
			if !re.MatchString(f.Digest) {
				t.Errorf("%s: digest %q does not match sha256:[a-f0-9]{64}", f.URI, f.Digest)
			}
		}
	}
}

func TestIndexer_Entries_DigestCorrectness(t *testing.T) {
	p := mustProvider(t, "testdata/valid")
	entries, err := skills.NewIndexer(p).Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	raw, err := os.ReadFile("testdata/valid/git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	sum := sha256.Sum256(raw)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])

	var got string
	for _, e := range entries {
		for _, f := range e.Resources.Files {
			if f.URI == "skill://git-workflow/SKILL.md" {
				got = f.Digest
			}
		}
	}
	if got != wantDigest {
		t.Errorf("git-workflow digest = %q, want %q", got, wantDigest)
	}
}

func TestIndexer_Entries_URISorted(t *testing.T) {
	p := mustProvider(t, "testdata/valid")
	entries, err := skills.NewIndexer(p).Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	urls := make([]string, len(entries))
	for i, e := range entries {
		urls[i] = e.URI
	}
	sorted := append([]string(nil), urls...)
	sort.Strings(sorted)
	if !equalSlices(urls, sorted) {
		t.Errorf("entries not URI-sorted: %v", urls)
	}
}

func TestIndexer_Entries_Empty(t *testing.T) {
	p, err := skills.NewProvider(skills.WithFS(fstest.MapFS{}))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	entries, err := skills.NewIndexer(p).Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if entries == nil {
		t.Errorf("entries slice should be non-nil when there are no skills")
	}
	if len(entries) != 0 {
		t.Errorf("entries should be empty, got %d", len(entries))
	}
}

func TestIndexer_CacheTTL_HitsAndMiss(t *testing.T) {
	mfs := singleSkillMapFS(t, time.Unix(1_700_000_000, 0))
	cfs := &countingFS{FS: mfs}
	p := mustProviderFromFS(t, cfs)

	idx := skills.NewIndexer(p, skills.WithIndexerCacheTTL(50*time.Millisecond))
	if _, err := idx.Entries(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	reads1 := atomic.LoadInt32(&cfs.openCount)
	if _, err := idx.Entries(); err != nil {
		t.Fatalf("Index #2 (within TTL): %v", err)
	}
	reads2 := atomic.LoadInt32(&cfs.openCount)
	if reads2 != reads1 {
		t.Errorf("Index() within TTL re-read SKILL.md: opens went %d -> %d", reads1, reads2)
	}

	time.Sleep(60 * time.Millisecond)
	if _, err := idx.Entries(); err != nil {
		t.Fatalf("Index #3 (after TTL): %v", err)
	}
	reads3 := atomic.LoadInt32(&cfs.openCount)
	if reads3 == reads2 {
		t.Errorf("Index() after TTL expiry did not re-read SKILL.md: opens stayed at %d", reads3)
	}
}

func TestIndexer_CacheMtimeInvalidates(t *testing.T) {
	original := time.Unix(1_700_000_000, 0)
	mfs := singleSkillMapFS(t, original)
	p := mustProviderFromFS(t, mfs)

	idx := skills.NewIndexer(p, skills.WithIndexerCacheTTL(time.Hour))
	first, err := idx.Entries()
	if err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(first))
	}

	// Mutate the SKILL.md bytes and bump mtime. Index() within TTL must
	// detect the mtime change and recompute the digest.
	mfs["solo/SKILL.md"].Data = []byte(`---
name: solo
description: A solitary skill, edited
---

new body
`)
	mfs["solo/SKILL.md"].ModTime = original.Add(time.Minute)

	second, err := idx.Entries()
	if err != nil {
		t.Fatalf("Index #2: %v", err)
	}
	if second[0].Resources.Files[0].Digest == first[0].Resources.Files[0].Digest {
		t.Errorf("digest did not change after mutating SKILL.md: %q", first[0].Resources.Files[0].Digest)
	}
}

func TestIndexer_ZeroMtimeFallback(t *testing.T) {
	// fstest.MapFS files whose ModTime is the zero value report mtime
	// as time.Time{}. With TTL set and noMtime true the cache must fall
	// back to TTL freshness; without TTL it must recompute every call to
	// avoid serving a permanently stale index.
	mfs := fstest.MapFS{
		"solo/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: solo
description: Zero-mtime fixture
---
`),
		},
	}
	cfs := &countingFS{FS: mfs}
	p := mustProviderFromFS(t, cfs)

	// No TTL → recompute every call.
	idx0 := skills.NewIndexer(p)
	if _, err := idx0.Entries(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	reads1 := atomic.LoadInt32(&cfs.openCount)
	if _, err := idx0.Entries(); err != nil {
		t.Fatalf("Index #2: %v", err)
	}
	reads2 := atomic.LoadInt32(&cfs.openCount)
	if reads2 == reads1 {
		t.Errorf("zero-mtime + zero-TTL: expected re-read, opens stayed at %d", reads2)
	}

	// TTL set → cache by TTL even though mtime cannot drive invalidation.
	idxT := skills.NewIndexer(p, skills.WithIndexerCacheTTL(time.Hour))
	if _, err := idxT.Entries(); err != nil {
		t.Fatalf("TTL Index #1: %v", err)
	}
	readsA := atomic.LoadInt32(&cfs.openCount)
	if _, err := idxT.Entries(); err != nil {
		t.Fatalf("TTL Index #2: %v", err)
	}
	readsB := atomic.LoadInt32(&cfs.openCount)
	if readsB != readsA {
		t.Errorf("zero-mtime + TTL: TTL hit should not re-read, opens went %d -> %d", readsA, readsB)
	}
}

// TestProvider_RegisterWith_MethodsExposed replaces the old index-resource
// test: enumeration is now two methods, and declaring the extension commits
// the server to both.
func TestProvider_RegisterWith_MethodsExposed(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")

	var lr skills.SkillsListResult
	res, err := c.Call(t.Context(), skills.MethodSkillsList, skills.SkillsListRequest{})
	if err != nil {
		t.Fatalf("skills/list: %v", err)
	}
	if err := res.Unmarshal(&lr); err != nil {
		t.Fatalf("decode skills/list: %v", err)
	}
	if len(lr.Skills) != 3 {
		t.Errorf("skills count = %d, want 3 (testdata/valid has 3 skills)", len(lr.Skills))
	}

	if _, err := c.Call(t.Context(), skills.MethodSkillsGet, skills.SkillsGetRequest{URI: lr.Skills[0].URI}); err != nil {
		t.Fatalf("skills/get: %v", err)
	}
}

// TestProvider_NoIndexResource pins the retirement: skill://index.json is no
// longer served, so a read of it must miss rather than return a stale catalog.
func TestProvider_NoIndexResource(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")

	defs, err := c.ListResources(t.Context())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	for _, d := range defs {
		if d.URI == "skill://index.json" {
			t.Errorf("skill://index.json is still registered; the 08-21 SEP retired it")
		}
	}
	if _, err := c.ReadResource(t.Context(), "skill://index.json"); err == nil {
		t.Error("reading skill://index.json succeeded; it must no longer be served")
	}
}

func TestProvider_WithIndexCacheTTL_ForwardsToIndexer(t *testing.T) {
	mfs := singleSkillMapFS(t, time.Unix(1_700_000_000, 0))
	cfs := &countingFS{FS: mfs}

	srv := server.NewServer(core.ServerInfo{Name: "skills-ttl-fwd", Version: "0.0.1"})
	p, err := skills.NewProvider(
		skills.WithFS(cfs),
		skills.WithIndexCacheTTL(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-ttl-fwd-client", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if _, err := c.Call(t.Context(), skills.MethodSkillsList, skills.SkillsListRequest{}); err != nil {
		t.Fatalf("skills/list #1: %v", err)
	}
	reads1 := atomic.LoadInt32(&cfs.openCount)
	if _, err := c.Call(t.Context(), skills.MethodSkillsList, skills.SkillsListRequest{}); err != nil {
		t.Fatalf("skills/list #2: %v", err)
	}
	reads2 := atomic.LoadInt32(&cfs.openCount)
	if reads2 != reads1 {
		t.Errorf("WithIndexCacheTTL did not forward cache to Indexer: opens %d -> %d", reads1, reads2)
	}
}

func TestIndexer_RegisterWith_AddsMethods(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "skills-indexer-only", Version: "0.0.1"})
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	skills.NewIndexer(p).RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-indexer-only-client", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	res, err := c.Call(t.Context(), skills.MethodSkillsList, skills.SkillsListRequest{})
	if err != nil {
		t.Fatalf("skills/list: %v", err)
	}
	var lr skills.SkillsListResult
	if err := res.Unmarshal(&lr); err != nil {
		t.Fatalf("decode skills/list: %v", err)
	}
	if len(lr.Skills) == 0 {
		t.Error("skills/list returned no entries")
	}
}

func TestIndexer_Entries_ConcurrentSafe(t *testing.T) {
	// Race-detector smoke test. The cache mutex is taken at the top of
	// Entries() and held through both isFresh() and buildEntries().
	// Multiple goroutines hammering Entries() with a short TTL must produce
	// no data race and consistent digests for the same artifact.
	p := mustProvider(t, "testdata/valid")
	idx := skills.NewIndexer(p, skills.WithIndexerCacheTTL(2*time.Millisecond))

	const goroutines = 16
	const iterations = 50

	first, err := idx.Entries()
	if err != nil {
		t.Fatalf("Entries #1: %v", err)
	}
	wantDigests := make(map[string]string)
	for _, e := range first {
		for _, f := range e.Resources.Files {
			wantDigests[f.URI] = f.Digest
		}
	}

	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			for i := 0; i < iterations; i++ {
				got, err := idx.Entries()
				if err != nil {
					errCh <- err
					return
				}
				for _, e := range got {
					for _, f := range e.Resources.Files {
						if want := wantDigests[f.URI]; f.Digest != want {
							errCh <- &digestDriftErr{url: f.URI, want: want, got: f.Digest}
							return
						}
					}
				}
			}
			errCh <- nil
		}()
	}
	for g := 0; g < goroutines; g++ {
		if err := <-errCh; err != nil {
			t.Fatalf("goroutine err: %v", err)
		}
	}
}

type digestDriftErr struct {
	url, want, got string
}

func (e *digestDriftErr) Error() string {
	return "digest drift for " + e.url + ": want " + e.want + ", got " + e.got
}

// --- helpers ---

func mustProvider(t *testing.T, dir string) *skills.Provider {
	t.Helper()
	p, err := skills.NewProvider(skills.WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewProvider(%s): %v", dir, err)
	}
	return p
}

func mustProviderFromFS(t *testing.T, fsys fs.FS) *skills.Provider {
	t.Helper()
	p, err := skills.NewProvider(skills.WithFS(fsys))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func singleSkillMapFS(t *testing.T, mtime time.Time) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"solo/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: solo
description: A solitary skill
---

body
`),
			ModTime: mtime,
		},
	}
}

func boot(t *testing.T, dir string) (*server.Server, *httptest.Server, *client.Client) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "skills-boot", Version: "0.0.1"})
	p, err := skills.NewProvider(skills.WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-boot-client", Version: "0.0.1"})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return srv, ts, c
}

// countingFS wraps an fs.FS and counts Open calls so cache hits/misses
// can be asserted without timing-sensitive comparisons. It implements
// fs.StatFS so the mtime check in Indexer.isFresh dispatches to the
// underlying FS via Stat rather than Open, keeping the openCount focused
// on actual full-file reads.
type countingFS struct {
	fs.FS
	openCount int32
	statCount int32
}

func (c *countingFS) Open(name string) (fs.File, error) {
	atomic.AddInt32(&c.openCount, 1)
	return c.FS.Open(name)
}

func (c *countingFS) Stat(name string) (fs.FileInfo, error) {
	atomic.AddInt32(&c.statCount, 1)
	return fs.Stat(c.FS, name)
}

// TestIndexer_WithMtimeChecksDisabled_SkipsStatOnCacheHit is the issue-576
// acceptance: with WithMtimeChecks(false) and a positive TTL, a cache hit
// does not fs.Stat the cataloged skills — the point of the option for a
// backing where stat is expensive.
func TestIndexer_WithMtimeChecksDisabled_SkipsStatOnCacheHit(t *testing.T) {
	cfs := &countingFS{FS: singleSkillMapFS(t, time.Unix(1_700_000_000, 0))}
	p := mustProviderFromFS(t, cfs)

	idx := skills.NewIndexer(p, skills.WithIndexerCacheTTL(time.Hour), skills.WithMtimeChecks(false))
	if _, err := idx.Entries(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	stats1 := atomic.LoadInt32(&cfs.statCount)

	if _, err := idx.Entries(); err != nil {
		t.Fatalf("Index #2 (within TTL): %v", err)
	}
	if stats2 := atomic.LoadInt32(&cfs.statCount); stats2 != stats1 {
		t.Errorf("WithMtimeChecks(false) cache hit still called fs.Stat: statCount %d -> %d", stats1, stats2)
	}
}

// TestIndexer_MtimeChecksDefaultOn_StatsOnCacheHit pins the default: with
// mtime checks on (the default), a cache hit does stat each skill to detect
// in-place edits. Contrast with the disabled case above.
func TestIndexer_MtimeChecksDefaultOn_StatsOnCacheHit(t *testing.T) {
	cfs := &countingFS{FS: singleSkillMapFS(t, time.Unix(1_700_000_000, 0))}
	p := mustProviderFromFS(t, cfs)

	idx := skills.NewIndexer(p, skills.WithIndexerCacheTTL(time.Hour)) // mtime checks default on
	if _, err := idx.Entries(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	stats1 := atomic.LoadInt32(&cfs.statCount)

	if _, err := idx.Entries(); err != nil {
		t.Fatalf("Index #2 (within TTL): %v", err)
	}
	if stats2 := atomic.LoadInt32(&cfs.statCount); stats2 == stats1 {
		t.Errorf("default mtime checks: cache hit did not fs.Stat (expected mtime comparison), statCount stayed %d", stats1)
	}
}
