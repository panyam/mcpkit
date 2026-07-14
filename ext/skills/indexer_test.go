package skills_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

func TestIndexer_Index_SchemaURI(t *testing.T) {
	p := mustProvider(t, "testdata/valid")
	idx, err := skills.NewIndexer(p).Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if idx.Schema != skills.IndexSchemaURI {
		t.Errorf("Schema = %q, want %q", idx.Schema, skills.IndexSchemaURI)
	}
}

func TestIndexer_Index_DigestFormat(t *testing.T) {
	p := mustProvider(t, "testdata/valid")
	idx, err := skills.NewIndexer(p).Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(idx.Skills) == 0 {
		t.Fatal("expected populated Skills")
	}
	re := regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	for _, e := range idx.Skills {
		if !re.MatchString(e.Digest) {
			t.Errorf("entry %q: digest %q does not match sha256:[a-f0-9]{64}", e.Name, e.Digest)
		}
	}
}

func TestIndexer_Index_DigestCorrectness(t *testing.T) {
	p := mustProvider(t, "testdata/valid")
	idx, err := skills.NewIndexer(p).Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	raw, err := os.ReadFile("testdata/valid/git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	sum := sha256.Sum256(raw)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])

	var got string
	for _, e := range idx.Skills {
		if e.URL == "skill://git-workflow/SKILL.md" {
			got = e.Digest
			break
		}
	}
	if got != wantDigest {
		t.Errorf("git-workflow digest = %q, want %q", got, wantDigest)
	}
}

func TestIndexer_Index_URLSorted(t *testing.T) {
	p := mustProvider(t, "testdata/valid")
	idx, err := skills.NewIndexer(p).Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	urls := make([]string, len(idx.Skills))
	for i, e := range idx.Skills {
		urls[i] = e.URL
	}
	sorted := append([]string(nil), urls...)
	sort.Strings(sorted)
	if !equalSlices(urls, sorted) {
		t.Errorf("entries not URL-sorted: %v", urls)
	}
}

func TestIndexer_Index_Empty(t *testing.T) {
	p, err := skills.NewProvider(skills.WithFS(fstest.MapFS{}))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	idx, err := skills.NewIndexer(p).Index()
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if idx.Schema != skills.IndexSchemaURI {
		t.Errorf("Schema = %q, want %q", idx.Schema, skills.IndexSchemaURI)
	}
	if idx.Skills == nil {
		t.Errorf("Skills slice should be non-nil for empty index")
	}
	if len(idx.Skills) != 0 {
		t.Errorf("Skills should be empty, got %d entries", len(idx.Skills))
	}
}

func TestIndexer_CacheTTL_HitsAndMiss(t *testing.T) {
	mfs := singleSkillMapFS(t, time.Unix(1_700_000_000, 0))
	cfs := &countingFS{FS: mfs}
	p := mustProviderFromFS(t, cfs)

	idx := skills.NewIndexer(p, skills.WithIndexerCacheTTL(50*time.Millisecond))
	if _, err := idx.Index(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	reads1 := atomic.LoadInt32(&cfs.openCount)
	if _, err := idx.Index(); err != nil {
		t.Fatalf("Index #2 (within TTL): %v", err)
	}
	reads2 := atomic.LoadInt32(&cfs.openCount)
	if reads2 != reads1 {
		t.Errorf("Index() within TTL re-read SKILL.md: opens went %d -> %d", reads1, reads2)
	}

	time.Sleep(60 * time.Millisecond)
	if _, err := idx.Index(); err != nil {
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
	first, err := idx.Index()
	if err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	if len(first.Skills) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(first.Skills))
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

	second, err := idx.Index()
	if err != nil {
		t.Fatalf("Index #2: %v", err)
	}
	if second.Skills[0].Digest == first.Skills[0].Digest {
		t.Errorf("digest did not change after mutating SKILL.md: %q", first.Skills[0].Digest)
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
	if _, err := idx0.Index(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	reads1 := atomic.LoadInt32(&cfs.openCount)
	if _, err := idx0.Index(); err != nil {
		t.Fatalf("Index #2: %v", err)
	}
	reads2 := atomic.LoadInt32(&cfs.openCount)
	if reads2 == reads1 {
		t.Errorf("zero-mtime + zero-TTL: expected re-read, opens stayed at %d", reads2)
	}

	// TTL set → cache by TTL even though mtime cannot drive invalidation.
	idxT := skills.NewIndexer(p, skills.WithIndexerCacheTTL(time.Hour))
	if _, err := idxT.Index(); err != nil {
		t.Fatalf("TTL Index #1: %v", err)
	}
	readsA := atomic.LoadInt32(&cfs.openCount)
	if _, err := idxT.Index(); err != nil {
		t.Fatalf("TTL Index #2: %v", err)
	}
	readsB := atomic.LoadInt32(&cfs.openCount)
	if readsB != readsA {
		t.Errorf("zero-mtime + TTL: TTL hit should not re-read, opens went %d -> %d", readsA, readsB)
	}
}

func TestProvider_RegisterWith_IndexExposed(t *testing.T) {
	srv, ts, c := boot(t, "testdata/valid")
	_ = ts

	defs, err := c.ListResources(t.Context())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	var found bool
	for _, d := range defs {
		if d.URI == skills.IndexURI {
			found = true
			if d.MimeType != "application/json" {
				t.Errorf("index MimeType = %q, want application/json", d.MimeType)
			}
			break
		}
	}
	if !found {
		t.Errorf("resources/list missing %q in %v", skills.IndexURI, urisOf(defs))
	}

	body, err := c.ReadResource(skills.IndexURI)
	if err != nil {
		t.Fatalf("ReadResource %q: %v", skills.IndexURI, err)
	}
	var idx skills.Index
	if err := json.Unmarshal([]byte(body), &idx); err != nil {
		t.Fatalf("Unmarshal index: %v\nbody=%s", err, body)
	}
	if idx.Schema != skills.IndexSchemaURI {
		t.Errorf("Schema = %q", idx.Schema)
	}
	if len(idx.Skills) != 3 {
		t.Errorf("Skills count = %d, want 3 (testdata/valid has 3 skills)", len(idx.Skills))
	}
	_ = srv
}

func TestProvider_WithoutIndex(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "skills-no-index", Version: "0.0.1"})
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithoutIndex(),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "skills-no-index-client", Version: "0.0.1"})
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	defs, err := c.ListResources(t.Context())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	for _, d := range defs {
		if d.URI == skills.IndexURI {
			t.Errorf("WithoutIndex did not suppress %q; resources = %v", skills.IndexURI, urisOf(defs))
		}
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
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if _, err := c.ReadResource(skills.IndexURI); err != nil {
		t.Fatalf("ReadResource #1: %v", err)
	}
	reads1 := atomic.LoadInt32(&cfs.openCount)
	if _, err := c.ReadResource(skills.IndexURI); err != nil {
		t.Fatalf("ReadResource #2: %v", err)
	}
	reads2 := atomic.LoadInt32(&cfs.openCount)
	if reads2 != reads1 {
		t.Errorf("WithIndexCacheTTL did not forward cache to Indexer: opens %d -> %d", reads1, reads2)
	}
}

func TestIndexer_RegisterWith_AddsIndexResource(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "skills-indexer-only", Version: "0.0.1"})
	p, err := skills.NewProvider(
		skills.WithDirectory("testdata/valid"),
		skills.WithoutIndex(),
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
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	body, err := c.ReadResource(skills.IndexURI)
	if err != nil {
		t.Fatalf("ReadResource %q: %v", skills.IndexURI, err)
	}
	if !strings.Contains(body, skills.IndexSchemaURI) {
		t.Errorf("index body missing $schema URI: %s", body)
	}
}

func TestIndexer_Index_ConcurrentSafe(t *testing.T) {
	// Race-detector smoke test. The cache mutex is taken at the top of
	// Index() and held through both isFresh() and build(). Multiple
	// goroutines hammering Index() with a short TTL must produce no
	// data race and consistent digests for the same artifact.
	p := mustProvider(t, "testdata/valid")
	idx := skills.NewIndexer(p, skills.WithIndexerCacheTTL(2*time.Millisecond))

	const goroutines = 16
	const iterations = 50

	first, err := idx.Index()
	if err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	wantDigests := make(map[string]string, len(first.Skills))
	for _, e := range first.Skills {
		wantDigests[e.URL] = e.Digest
	}

	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			for i := 0; i < iterations; i++ {
				got, err := idx.Index()
				if err != nil {
					errCh <- err
					return
				}
				for _, e := range got.Skills {
					if want := wantDigests[e.URL]; e.Digest != want {
						errCh <- &digestDriftErr{url: e.URL, want: want, got: e.Digest}
						return
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
	if err := c.Connect(); err != nil {
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
	if _, err := idx.Index(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	stats1 := atomic.LoadInt32(&cfs.statCount)

	if _, err := idx.Index(); err != nil {
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
	if _, err := idx.Index(); err != nil {
		t.Fatalf("Index #1: %v", err)
	}
	stats1 := atomic.LoadInt32(&cfs.statCount)

	if _, err := idx.Index(); err != nil {
		t.Fatalf("Index #2 (within TTL): %v", err)
	}
	if stats2 := atomic.LoadInt32(&cfs.statCount); stats2 == stats1 {
		t.Errorf("default mtime checks: cache hit did not fs.Stat (expected mtime comparison), statCount stayed %d", stats1)
	}
}
