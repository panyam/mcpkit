package events

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryQuotaStore_ReserveBelowMaxGrants(t *testing.T) {
	s := NewInMemoryQuotaStore()
	resp, err := s.ReserveQuota(context.Background(), ReserveQuotaRequest{
		Principal: "alice", Key: "chat.message", Max: 3,
	})
	require.NoError(t, err)
	assert.True(t, resp.Granted)
	assert.Equal(t, 1, resp.Count)
}

func TestInMemoryQuotaStore_ReserveAtMaxDenies(t *testing.T) {
	s := NewInMemoryQuotaStore()
	for i := 0; i < 2; i++ {
		resp, err := s.ReserveQuota(context.Background(), ReserveQuotaRequest{
			Principal: "alice", Key: "chat.message", Max: 2,
		})
		require.NoError(t, err)
		require.True(t, resp.Granted)
	}
	denied, err := s.ReserveQuota(context.Background(), ReserveQuotaRequest{
		Principal: "alice", Key: "chat.message", Max: 2,
	})
	require.NoError(t, err)
	assert.False(t, denied.Granted, "third reservation must be denied")
	assert.Equal(t, 2, denied.Count, "denied response carries the count that blocked it")
}

func TestInMemoryQuotaStore_ReleaseDecrements(t *testing.T) {
	s := NewInMemoryQuotaStore()
	ctx := context.Background()
	_, _ = s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key:       "e", Max: 5})
	_, _ = s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key:       "e", Max: 5})
	_, _ = s.ReleaseQuota(ctx, ReleaseQuotaRequest{Principal: "alice", Key:       "e"})

	resp, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "alice", Key:       "e"})
	assert.Equal(t, 1, resp.Count)
}

func TestInMemoryQuotaStore_ReleaseAtZeroIsNoOp(t *testing.T) {
	s := NewInMemoryQuotaStore()
	ctx := context.Background()
	// Release before any Reserve must be a silent no-op (no underflow,
	// no panic). The contract is the QuotaStore's, not the wrapper's.
	_, err := s.ReleaseQuota(ctx, ReleaseQuotaRequest{Principal: "alice", Key:       "e"})
	require.NoError(t, err)
	resp, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "alice", Key:       "e"})
	assert.Equal(t, 0, resp.Count, "release-at-zero must not push the count negative")
}

func TestInMemoryQuotaStore_KeyScopedByPrincipalAndKey(t *testing.T) {
	s := NewInMemoryQuotaStore()
	ctx := context.Background()
	_, _ = s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key:       "chat.message", Max: 10})
	_, _ = s.ReserveQuota(ctx, ReserveQuotaRequest{Principal: "alice", Key:       "chat.message", Max: 10})

	// Other (principal, eventName) tuples must be unaffected.
	cross1, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "alice", Key:       "alert.fired"})
	cross2, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "bob", Key:       "chat.message"})
	assert.Equal(t, 0, cross1.Count)
	assert.Equal(t, 0, cross2.Count)

	own, _ := s.CountQuota(ctx, CountQuotaRequest{Principal: "alice", Key:       "chat.message"})
	assert.Equal(t, 2, own.Count)
}

// spyQuotaStore wraps the in-memory store with a call recorder so tests
// can assert that Quota routes through the seam in the expected order.
type spyQuotaStore struct {
	inner QuotaStore
	mu    sync.Mutex
	calls []string
}

func newSpyQuotaStore() *spyQuotaStore {
	return &spyQuotaStore{inner: NewInMemoryQuotaStore()}
}

func (s *spyQuotaStore) record(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, op)
}

func (s *spyQuotaStore) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *spyQuotaStore) ReserveQuota(ctx context.Context, req ReserveQuotaRequest) (ReserveQuotaResponse, error) {
	s.record("Reserve")
	return s.inner.ReserveQuota(ctx, req)
}
func (s *spyQuotaStore) ReleaseQuota(ctx context.Context, req ReleaseQuotaRequest) (ReleaseQuotaResponse, error) {
	s.record("Release")
	return s.inner.ReleaseQuota(ctx, req)
}
func (s *spyQuotaStore) CountQuota(ctx context.Context, req CountQuotaRequest) (CountQuotaResponse, error) {
	s.record("Count")
	return s.inner.CountQuota(ctx, req)
}

func TestQuota_UsesCustomStore_RoutesReserveAndRelease(t *testing.T) {
	spy := newSpyQuotaStore()
	q := NewQuota(
		WithMaxSubscriptionsPerPrincipal("chat.message", 2),
		WithQuotaStore(spy),
	)

	require.NoError(t, q.Reserve("alice", "chat.message"))
	q.Release("alice", "chat.message")

	calls := spy.snapshot()
	require.Equal(t, []string{"Reserve", "Release"}, calls,
		"Quota wrapper must route Reserve then Release through the store in that order")
}

func TestQuota_UncappedEventTypeDoesNotCallStore(t *testing.T) {
	// When no cap is configured, the wrapper short-circuits and never
	// calls the store — keeps the store from accumulating state for
	// uncapped event types.
	spy := newSpyQuotaStore()
	q := NewQuota(WithQuotaStore(spy))

	require.NoError(t, q.Reserve("alice", "uncapped.event"))
	q.Release("alice", "uncapped.event")

	assert.Empty(t, spy.snapshot(),
		"wrapper must not call the store for uncapped event types")
}

func TestQuota_DefaultStoreIsInMemory(t *testing.T) {
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("e", 1))
	require.NoError(t, q.Reserve("alice", "e"))

	err := q.Reserve("alice", "e")
	require.Error(t, err, "second Reserve at cap=1 must error")
	assert.True(t, errors.Is(err, ErrTooManySubscriptions),
		"error must wrap ErrTooManySubscriptions for callers using errors.Is")
}

func TestQuota_WithQuotaStoreNilFallsBackToDefault(t *testing.T) {
	q := NewQuota(
		WithMaxSubscriptionsPerPrincipal("e", 1),
		WithQuotaStore(nil),
	)
	require.NoError(t, q.Reserve("alice", "e"))
	err := q.Reserve("alice", "e")
	require.Error(t, err)
}
