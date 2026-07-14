package events

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInProcessCursors_PerSourceMonotone(t *testing.T) {
	p := NewInProcessCursors()
	ctx := context.Background()
	// Each source counts independently, starting at 1.
	for _, src := range []string{"a", "b"} {
		for want := 1; want <= 3; want++ {
			got, err := p.Next(ctx, src)
			require.NoError(t, err)
			assert.Equal(t, strconv.Itoa(want), got, "source %s", src)
		}
	}
}

// fakeIncr is a minimal in-memory Incrementer (models a shared counter
// such as a Redis INCR).
type fakeIncr struct {
	mu   sync.Mutex
	vals map[string]int64
	keys []string
}

func (f *fakeIncr) Incr(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.vals == nil {
		f.vals = map[string]int64{}
	}
	f.vals[key]++
	f.keys = append(f.keys, key)
	return f.vals[key], nil
}

func TestInt64IncrCursors_UsesPrefixedKeyAndIsMonotone(t *testing.T) {
	incr := &fakeIncr{}
	p := NewInt64IncrCursors(incr, "")
	ctx := context.Background()
	c1, err := p.Next(ctx, "chat.message")
	require.NoError(t, err)
	c2, err := p.Next(ctx, "chat.message")
	require.NoError(t, err)
	assert.Equal(t, "1", c1)
	assert.Equal(t, "2", c2)
	assert.Equal(t, DefaultCursorKeyPrefix+"chat.message", incr.keys[0])
}

// TestCursorMintingStore_MultiWriterGapFree is the #833 fix: N YieldingSources
// for the SAME source share one cursor-minting store — the whole-enchilada
// topology where `make inject` round-robins across N replicas. With the store
// assigning cursors on write, every event gets a globally-unique, strictly-
// increasing cursor, so events/poll resume is gap-free across writers. (The
// sibling TestYieldingSource_EventIDsUniqueAcrossSources documents the
// collision the per-replica InProcess default has.)
func TestCursorMintingStore_MultiWriterGapFree(t *testing.T) {
	const replicas, perReplica = 4, 25
	store := NewCursorMintingInMemoryStore(0)
	def := EventDef{Name: "chat.message"}
	ctx := context.Background()

	var yields []func(context.Context, map[string]string) error
	for r := 0; r < replicas; r++ {
		_, y := NewYieldingSource[map[string]string](def, WithEventBufferStore(store))
		yields = append(yields, y)
	}
	for i := 0; i < replicas*perReplica; i++ {
		require.NoError(t, yields[i%replicas](ctx, map[string]string{"k": "v"}))
	}

	resp, err := store.Poll(ctx, PollEventsRequest{SourceName: def.Name, Cursor: "", Limit: 10_000})
	require.NoError(t, err)
	require.Len(t, resp.Events, replicas*perReplica)

	seen := make(map[string]bool, len(resp.Events))
	var prev int64
	for i, e := range resp.Events {
		require.NotNil(t, e.Cursor, "store-minted event must carry a cursor")
		assert.False(t, seen[*e.Cursor], "duplicate cursor across replicas: %s", *e.Cursor)
		seen[*e.Cursor] = true
		n, err := strconv.ParseInt(*e.Cursor, 10, 64)
		require.NoError(t, err)
		if i > 0 {
			assert.Greater(t, n, prev, "cursors must be strictly increasing")
		}
		prev = n
	}

	// Resume from a mid cursor returns exactly the strictly-later tail.
	midEvents := resp.Events[:10]
	midCursor := *midEvents[len(midEvents)-1].Cursor
	tail, err := store.Poll(ctx, PollEventsRequest{SourceName: def.Name, Cursor: midCursor, Limit: 10_000})
	require.NoError(t, err)
	assert.Len(t, tail.Events, replicas*perReplica-10, "resume must be gap-free with no dupes")
}

// TestCursorMintingStore_SurvivesWriterRestart covers the other half of #833:
// the per-replica InProcess counter resets to 1 on restart and collides with
// already-buffered events. A cursor-minting store outlives any single writer,
// so a fresh source instance (a restarted replica) keeps minting strictly
// after the existing high-water mark.
func TestCursorMintingStore_SurvivesWriterRestart(t *testing.T) {
	store := NewCursorMintingInMemoryStore(0)
	def := EventDef{Name: "chat.message"}
	ctx := context.Background()

	_, y1 := NewYieldingSource[map[string]string](def, WithEventBufferStore(store))
	for i := 0; i < 5; i++ {
		require.NoError(t, y1(ctx, map[string]string{"k": "v"}))
	}
	before, err := store.Latest(ctx, LatestCursorRequest{SourceName: def.Name})
	require.NoError(t, err)
	high, _ := strconv.ParseInt(before.Cursor, 10, 64)

	// Restart: a new source instance, same durable store.
	_, y2 := NewYieldingSource[map[string]string](def, WithEventBufferStore(store))
	require.NoError(t, y2(ctx, map[string]string{"k": "v2"}))

	after, err := store.Latest(ctx, LatestCursorRequest{SourceName: def.Name})
	require.NoError(t, err)
	n, _ := strconv.ParseInt(after.Cursor, 10, 64)
	assert.Greater(t, n, high, "post-restart cursor must exceed the pre-restart high-water mark, not reset")
}

// fixedProvider mints from a recognizable high base so a test can tell a
// provider-minted cursor apart from a store-minted one.
type fixedProvider struct{ n atomic.Int64 }

func (p *fixedProvider) Next(_ context.Context, _ string) (string, error) {
	return strconv.FormatInt(1000+p.n.Add(1), 10), nil
}

// TestYield_ExplicitProviderWinsOverProvidingStore verifies the precedence: an
// explicitly-set CursorProvider mints even when the store could provide.
func TestYield_ExplicitProviderWinsOverProvidingStore(t *testing.T) {
	store := NewCursorMintingInMemoryStore(0)
	def := EventDef{Name: "chat.message"}
	ctx := context.Background()

	src, y := NewYieldingSource[fakePayload](def,
		WithEventBufferStore(store),
		WithCursorProvider(&fixedProvider{}),
	)
	require.NoError(t, y(ctx, fakePayload{Msg: "a"}))
	require.NoError(t, y(ctx, fakePayload{Msg: "b"}))

	pr := src.Poll("", 10)
	require.Len(t, pr.Events, 2)
	// 1001/1002 from the provider, not 1/2 the store would have assigned.
	assert.Equal(t, "1001", pr.Events[0].CursorStr())
	assert.Equal(t, "1002", pr.Events[1].CursorStr())
}

// TestYield_DefaultInProcessCursorsUnchanged pins the historical behavior: with
// no store and no explicit provider, cursors count 1, 2, 3 per source.
func TestYield_DefaultInProcessCursorsUnchanged(t *testing.T) {
	src, y := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	ctx := context.Background()
	require.NoError(t, y(ctx, fakePayload{Msg: "a"}))
	require.NoError(t, y(ctx, fakePayload{Msg: "b"}))

	pr := src.Poll("", 10)
	require.Len(t, pr.Events, 2)
	assert.Equal(t, "1", pr.Events[0].CursorStr())
	assert.Equal(t, "2", pr.Events[1].CursorStr())
}
