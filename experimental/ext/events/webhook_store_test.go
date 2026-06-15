package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTarget(keyByte byte) WebhookTarget {
	return WebhookTarget{
		CanonicalKey: []byte{keyByte},
		ID:           "sub_test_" + string([]byte{keyByte}),
		URL:          "http://example.test/sink",
		Secret:       "whsec_test_secret_xxxxxxxxxxxxxxxxxxxx",
		ExpiresAt:    ptrTime(time.Now().Add(time.Hour)),
		EventName:    "test.event",
		Status:       DeliveryStatus{Active: true},
	}
}

func TestInMemoryWebhookStore_SaveThenGetRoundTrip(t *testing.T) {
	s := NewInMemoryWebhookStore()
	want := newTestTarget('a')
	_, err := s.SaveWebhook(context.Background(), SaveWebhookRequest{Target: want})
	require.NoError(t, err)

	resp, err := s.GetWebhook(context.Background(), GetWebhookRequest{CanonicalKey: want.CanonicalKey})
	require.NoError(t, err)
	require.True(t, resp.Found)
	assert.Equal(t, want.ID, resp.Target.ID)
	assert.Equal(t, want.URL, resp.Target.URL)
	assert.Equal(t, want.Secret, resp.Target.Secret)
	require.NotNil(t, resp.Target.ExpiresAt)
	assert.WithinDuration(t, *want.ExpiresAt, *resp.Target.ExpiresAt, time.Millisecond)
}

func TestInMemoryWebhookStore_DeleteSemantics(t *testing.T) {
	s := NewInMemoryWebhookStore()
	want := newTestTarget('b')
	_, _ = s.SaveWebhook(context.Background(), SaveWebhookRequest{Target: want})

	resp, err := s.DeleteWebhook(context.Background(), DeleteWebhookRequest{CanonicalKey: want.CanonicalKey})
	require.NoError(t, err)
	require.True(t, resp.Found)
	assert.Equal(t, want.ID, resp.Removed.ID)

	// Delete is idempotent — second call returns Found=false, no error,
	// no mutation. Matches Go map delete semantics.
	resp2, err := s.DeleteWebhook(context.Background(), DeleteWebhookRequest{CanonicalKey: want.CanonicalKey})
	require.NoError(t, err)
	assert.False(t, resp2.Found)
	assert.Equal(t, WebhookTarget{}, resp2.Removed)
}

func TestInMemoryWebhookStore_ListReturnsSnapshotNotInternalSlice(t *testing.T) {
	s := NewInMemoryWebhookStore()
	_, _ = s.SaveWebhook(context.Background(), SaveWebhookRequest{Target: newTestTarget('a')})
	_, _ = s.SaveWebhook(context.Background(), SaveWebhookRequest{Target: newTestTarget('b')})

	first, _ := s.ListWebhooks(context.Background(), ListWebhooksRequest{})
	require.Len(t, first.Targets, 2)

	// Mutate the returned slice; the next List must still see two.
	first.Targets = first.Targets[:0]
	second, _ := s.ListWebhooks(context.Background(), ListWebhooksRequest{})
	assert.Len(t, second.Targets, 2, "ListWebhooks must return a snapshot — mutating the returned slice cannot affect store state")
}

func TestInMemoryWebhookStore_CountTracksSavesAndDeletes(t *testing.T) {
	s := NewInMemoryWebhookStore()
	zero, _ := s.CountWebhooks(context.Background(), CountWebhooksRequest{})
	assert.Equal(t, 0, zero.Count)

	_, _ = s.SaveWebhook(context.Background(), SaveWebhookRequest{Target: newTestTarget('a')})
	_, _ = s.SaveWebhook(context.Background(), SaveWebhookRequest{Target: newTestTarget('b')})
	twoAfter, _ := s.CountWebhooks(context.Background(), CountWebhooksRequest{})
	assert.Equal(t, 2, twoAfter.Count)

	// Save of existing key is upsert — count stays the same.
	_, _ = s.SaveWebhook(context.Background(), SaveWebhookRequest{Target: newTestTarget('a')})
	stillTwo, _ := s.CountWebhooks(context.Background(), CountWebhooksRequest{})
	assert.Equal(t, 2, stillTwo.Count)

	_, _ = s.DeleteWebhook(context.Background(), DeleteWebhookRequest{CanonicalKey: []byte{'a'}})
	oneAfter, _ := s.CountWebhooks(context.Background(), CountWebhooksRequest{})
	assert.Equal(t, 1, oneAfter.Count)
}

// spyStore wraps the in-memory store with a call recorder so tests can
// assert that WebhookRegistry exercises the seam in the expected
// sequence on Register → Deliver → Unregister.
type spyStore struct {
	inner WebhookStore
	mu    sync.Mutex
	calls []string
}

func newSpyStore() *spyStore {
	return &spyStore{inner: NewInMemoryWebhookStore()}
}

func (s *spyStore) record(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, op)
}

func (s *spyStore) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *spyStore) GetWebhook(ctx context.Context, req GetWebhookRequest) (GetWebhookResponse, error) {
	s.record("Get")
	return s.inner.GetWebhook(ctx, req)
}
func (s *spyStore) SaveWebhook(ctx context.Context, req SaveWebhookRequest) (SaveWebhookResponse, error) {
	s.record("Save")
	return s.inner.SaveWebhook(ctx, req)
}
func (s *spyStore) DeleteWebhook(ctx context.Context, req DeleteWebhookRequest) (DeleteWebhookResponse, error) {
	s.record("Delete")
	return s.inner.DeleteWebhook(ctx, req)
}
func (s *spyStore) ListWebhooks(ctx context.Context, req ListWebhooksRequest) (ListWebhooksResponse, error) {
	s.record("List")
	return s.inner.ListWebhooks(ctx, req)
}
func (s *spyStore) CountWebhooks(ctx context.Context, req CountWebhooksRequest) (CountWebhooksResponse, error) {
	s.record("Count")
	return s.inner.CountWebhooks(ctx, req)
}

func TestWebhookRegistry_UsesCustomStore_RegisterAndUnregister(t *testing.T) {
	spy := newSpyStore()
	r := NewWebhookRegistry(WithWebhookStore(spy))

	canonical := []byte("canonical-key-x")
	r.Register(RegisterParams{
		CanonicalKey: canonical,
		DerivedID:    "sub_x",
		URL:          "http://example.test/sink",
		Secret:       "whsec_xxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		EventName:    "test.event",
	})
	r.Unregister(canonical)

	calls := spy.snapshot()
	// Register's first action is pruneExpiredLocked (List). Then a
	// Get to detect refresh-vs-new, then a Save for the new target.
	// Unregister fires one Delete.
	require.Contains(t, calls, "List", "pruneExpired must list")
	require.Contains(t, calls, "Get", "Register must Get to detect refresh-vs-new")
	require.Contains(t, calls, "Save", "Register must Save the new target")
	require.Contains(t, calls, "Delete", "Unregister must Delete by canonical key")
}

func TestWebhookRegistry_DefaultStoreIsInMemory(t *testing.T) {
	// Behavior-preserving property: a registry constructed without
	// WithWebhookStore must use NewInMemoryWebhookStore as the default,
	// and the Targets() snapshot must work without further wiring.
	r := NewWebhookRegistry()
	canonical := []byte("default-store-test")
	r.Register(RegisterParams{
		CanonicalKey: canonical,
		DerivedID:    "sub_default",
		URL:          "http://example.test/sink",
		Secret:       "whsec_xxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		EventName:    "test.event",
	})
	targets := r.Targets()
	require.Len(t, targets, 1)
	assert.Equal(t, "sub_default", targets[0].ID)
}

func TestWebhookRegistry_WithWebhookStoreNilFallsBackToDefault(t *testing.T) {
	// Passing nil keeps the constructor's default — no panic, no
	// surprise behavior.
	r := NewWebhookRegistry(WithWebhookStore(nil))
	canonical := []byte("nil-store-test")
	r.Register(RegisterParams{
		CanonicalKey: canonical,
		DerivedID:    "sub_nil",
		URL:          "http://example.test/sink",
		Secret:       "whsec_xxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		EventName:    "test.event",
	})
	assert.Len(t, r.Targets(), 1)
}
