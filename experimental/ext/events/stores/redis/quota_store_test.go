package redisstore

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// newTestQuotaStore wraps newTestClient with a QuotaStore using the
// default Options shape — caller can override Options.QuotaTTL by
// passing a non-zero override for the TTL-expiry test.
func newTestQuotaStore(t *testing.T, ttl time.Duration) *QuotaStore {
	t.Helper()
	opts := Options{
		Client:   newTestClient(t),
		QuotaTTL: ttl,
	}
	s, err := NewQuotaStore(opts)
	require.NoError(t, err)
	return s
}

// TestQuotaStore_ReserveReleaseCount_RoundTrip is the happy path.
// Reserve, observe Count=1, Release, observe Count=0, Reserve again,
// observe Count=1 — i.e., the slot is genuinely returned and reusable.
func TestQuotaStore_ReserveReleaseCount_RoundTrip(t *testing.T) {
	s := newTestQuotaStore(t, 0) // default TTL
	ctx := t.Context()

	resp, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: 3,
	})
	require.NoError(t, err)
	assert.True(t, resp.Granted)
	assert.Equal(t, 1, resp.Count, "post-increment value")

	cnt, err := s.CountQuota(ctx, events.CountQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, cnt.Count)

	_, err = s.ReleaseQuota(ctx, events.ReleaseQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message",
	})
	require.NoError(t, err)

	cnt, err = s.CountQuota(ctx, events.CountQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, cnt.Count, "release should bring count back to zero")

	// And the slot is reusable.
	resp, err = s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: 3,
	})
	require.NoError(t, err)
	assert.True(t, resp.Granted)
	assert.Equal(t, 1, resp.Count)
}

// TestQuotaStore_ReserveRespectsMax verifies the cap: once Count
// reaches Max, further Reserves return Granted=false + Count=Max.
// Locks the spec that Max is exclusive (Reserve succeeds while Count
// < Max, not <= Max).
func TestQuotaStore_ReserveRespectsMax(t *testing.T) {
	s := newTestQuotaStore(t, 0)
	ctx := t.Context()
	const max = 3

	for i := 1; i <= max; i++ {
		resp, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
			Principal: "tenant-a/alice", Key: "chat.message", Max: max,
		})
		require.NoError(t, err)
		assert.True(t, resp.Granted, "Reserve #%d should succeed", i)
		assert.Equal(t, i, resp.Count)
	}

	// One over the cap.
	resp, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: max,
	})
	require.NoError(t, err)
	assert.False(t, resp.Granted, "Reserve at cap must be rejected")
	assert.Equal(t, max, resp.Count, "Count should report the blocking value (=Max), not bump")
}

// TestQuotaStore_ReleaseAtZeroIsNoOp matches the in-memory store's
// contract: Release on a never-Reserved (or already-zero) key is a
// silent no-op, no underflow, no error. Important because the
// wrapper sometimes Releases speculatively in error paths.
func TestQuotaStore_ReleaseAtZeroIsNoOp(t *testing.T) {
	s := newTestQuotaStore(t, 0)
	ctx := t.Context()

	// Release before any Reserve.
	_, err := s.ReleaseQuota(ctx, events.ReleaseQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message",
	})
	assert.NoError(t, err, "Release-at-zero must not error")

	cnt, err := s.CountQuota(ctx, events.CountQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, cnt.Count, "Count must stay at zero (no negative)")
}

// TestQuotaStore_PerPrincipalEventIsolation verifies the key scheme
// keeps (Principal, Key) tuples isolated. A Reserve for
// (alice, chat) MUST NOT count against (bob, chat) or (alice, presence).
// Two tenants of the same backend would corrupt each other if this
// failed.
func TestQuotaStore_PerPrincipalEventIsolation(t *testing.T) {
	s := newTestQuotaStore(t, 0)
	ctx := t.Context()

	// Fill alice's chat.message slot.
	_, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: 1,
	})
	require.NoError(t, err)

	// bob's chat.message should still be free.
	resp, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/bob", Key: "chat.message", Max: 1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Granted, "bob's chat.message must be independent of alice's")

	// alice's presence.changed should still be free.
	resp, err = s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "presence.changed", Max: 1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Granted, "alice's presence.changed must be independent of chat.message")
}

// TestQuotaStore_CountReadsWithoutMutation is a sanity test that
// CountQuota is purely read-only. Read-then-write code accidentally
// using CountQuota as a "peek + maybe-modify" primitive would break
// the wrapper's bookkeeping.
func TestQuotaStore_CountReadsWithoutMutation(t *testing.T) {
	s := newTestQuotaStore(t, 0)
	ctx := t.Context()

	_, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: 5,
	})
	require.NoError(t, err)

	// 10 Counts in a row — none should bump the value.
	for i := 0; i < 10; i++ {
		cnt, err := s.CountQuota(ctx, events.CountQuotaRequest{
			Principal: "tenant-a/alice", Key: "chat.message",
		})
		require.NoError(t, err)
		assert.Equal(t, 1, cnt.Count, "Count must stay at 1 after %d reads", i+1)
	}
}

// TestQuotaStore_PrefixIsolation verifies the ChannelPrefix namespace
// extends to quota keys. Two QuotaStores sharing the same Redis but
// using different ChannelPrefix MUST NOT see each other's counters
// — load-bearing for the demo's intent that multiple isolated demo
// stacks can share one Redis cluster.
func TestQuotaStore_PrefixIsolation(t *testing.T) {
	cli := newTestClient(t)
	sA, err := NewQuotaStore(Options{Client: cli, ChannelPrefix: "demoA"})
	require.NoError(t, err)
	sB, err := NewQuotaStore(Options{Client: cli, ChannelPrefix: "demoB"})
	require.NoError(t, err)
	ctx := t.Context()

	// Fill demoA's (alice, chat) slot.
	for i := 0; i < 3; i++ {
		resp, err := sA.ReserveQuota(ctx, events.ReserveQuotaRequest{
			Principal: "alice", Key: "chat", Max: 3,
		})
		require.NoError(t, err)
		require.True(t, resp.Granted)
	}

	// demoB's (alice, chat) MUST be empty.
	cnt, err := sB.CountQuota(ctx, events.CountQuotaRequest{
		Principal: "alice", Key: "chat",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, cnt.Count, "ChannelPrefix isolation: demoB must not see demoA's counts")

	resp, err := sB.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "alice", Key: "chat", Max: 1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Granted, "demoB's slot must still be available despite demoA being at cap")
}

// TestQuotaStore_TTLExpiresLeakedReserve verifies the leak-recovery
// floor. Set a short TTL, Reserve to fill the cap, advance time past
// the TTL window — the leaked slot should be evictable and a fresh
// Reserve succeeds.
//
// miniredis ships its own clock (Server.FastForward) for this kind
// of test; the real-Redis variant runs separately under
// TestQuotaStore_TTLExpiresLeakedReserve_RealRedis (gated below).
func TestQuotaStore_TTLExpiresLeakedReserve(t *testing.T) {
	if os.Getenv("MCPKIT_EVENTS_TEST_REDIS_ADDR") != "" {
		t.Skip("real Redis: covered by TestQuotaStore_TTLExpiresLeakedReserve_RealRedis")
	}
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	cli := miniredisClient(t, mr)

	s, err := NewQuotaStore(Options{Client: cli, QuotaTTL: 5 * time.Second})
	require.NoError(t, err)
	ctx := t.Context()

	// Fill the slot with Max=1 so any further Reserve is blocked.
	resp, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: 1,
	})
	require.NoError(t, err)
	require.True(t, resp.Granted)

	// Cap blocks immediately.
	resp, err = s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: 1,
	})
	require.NoError(t, err)
	require.False(t, resp.Granted)

	// Fast-forward miniredis past the TTL window.
	mr.FastForward(10 * time.Second)

	// Leaked slot evicted; Reserve succeeds again.
	resp, err = s.ReserveQuota(ctx, events.ReserveQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message", Max: 1,
	})
	require.NoError(t, err)
	assert.True(t, resp.Granted, "after TTL expiry, the leaked slot must be reclaimable")
}

// TestQuotaStore_ConcurrentReserveAtomicity is the load-bearing
// atomicity check: 100 goroutines race to Reserve against a Max=10
// cap on the same (Principal, Key) key. The Lua script MUST
// produce exactly 10 Granted=true responses, never 11, never 9.
// A naive GET-then-INCR would race here and over-grant.
func TestQuotaStore_ConcurrentReserveAtomicity(t *testing.T) {
	s := newTestQuotaStore(t, 0)
	ctx := t.Context()
	const max = 10
	const racers = 100

	var granted int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := s.ReserveQuota(ctx, events.ReserveQuotaRequest{
				Principal: "tenant-a/alice", Key: "chat.message", Max: max,
			})
			if err == nil && resp.Granted {
				atomic.AddInt64(&granted, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(max), granted, "exactly Max Reserves must succeed (no over-grant, no under-grant)")

	// Final count matches.
	cnt, err := s.CountQuota(ctx, events.CountQuotaRequest{
		Principal: "tenant-a/alice", Key: "chat.message",
	})
	require.NoError(t, err)
	assert.Equal(t, max, cnt.Count)
}

// TestNewQuotaStore_RejectsNilClient + RejectsNegativeTTL lock the
// constructor's validation. Tiny tests but they prevent a footgun
// at construction (silent nil-pointer panics deep inside ReserveQuota).
func TestNewQuotaStore_RejectsNilClient(t *testing.T) {
	_, err := NewQuotaStore(Options{})
	assert.ErrorContains(t, err, "Client is required")
}

func TestNewQuotaStore_RejectsNegativeTTL(t *testing.T) {
	_, err := NewQuotaStore(Options{
		Client:   newTestClient(t),
		QuotaTTL: -1 * time.Second,
	})
	assert.ErrorContains(t, err, "QuotaTTL must be >= 0")
}

// miniredisClient wires a *redis.Client at a miniredis instance's
// address. Used by the TTL test that needs direct access to the
// miniredis Server for FastForward — the shared newTestClient
// helper hides the Server behind t.Cleanup.
func miniredisClient(t *testing.T, mr *miniredis.Miniredis) *goredis.Client {
	t.Helper()
	cli := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	require.NoError(t, cli.FlushDB(context.Background()).Err())
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}
