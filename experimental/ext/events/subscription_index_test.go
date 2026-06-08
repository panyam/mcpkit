package events

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemorySubscriptionIndex_AddLookupRoundTrip(t *testing.T) {
	idx := NewInMemorySubscriptionIndex()
	var delivered Event
	deliver := func(e Event) { delivered = e }

	_, err := idx.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: "sub_a",
		Mode:           DeliveryModePush,
		Deliver:        deliver,
	})
	require.NoError(t, err)

	resp, err := idx.LookupSubscription(context.Background(), LookupSubscriptionRequest{SubscriptionID: "sub_a"})
	require.NoError(t, err)
	require.True(t, resp.Found)
	assert.Equal(t, DeliveryModePush, resp.Mode)
	require.NotNil(t, resp.Deliver)

	resp.Deliver(Event{EventID: "evt_x"})
	assert.Equal(t, "evt_x", delivered.EventID)
}

func TestInMemorySubscriptionIndex_ReplaceOnSameID(t *testing.T) {
	idx := NewInMemorySubscriptionIndex()
	var which string

	_, _ = idx.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: "sub_a",
		Mode:           DeliveryModePush,
		Deliver:        func(Event) { which = "first" },
	})
	_, _ = idx.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: "sub_a",
		Mode:           DeliveryModeWebhook,
		Deliver:        func(Event) { which = "second" },
	})

	resp, _ := idx.LookupSubscription(context.Background(), LookupSubscriptionRequest{SubscriptionID: "sub_a"})
	resp.Deliver(Event{})
	assert.Equal(t, "second", which, "second Add must replace the first; latest wiring wins")
	assert.Equal(t, DeliveryModeWebhook, resp.Mode, "mode must reflect the replacing entry")

	count, _ := idx.CountSubscriptions(context.Background(), CountSubscriptionsRequest{})
	assert.Equal(t, 1, count.Count, "replace must not duplicate")
}

func TestInMemorySubscriptionIndex_RemoveDropsEntry(t *testing.T) {
	idx := NewInMemorySubscriptionIndex()
	_, _ = idx.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: "sub_a", Mode: DeliveryModePush, Deliver: func(Event) {},
	})
	_, _ = idx.RemoveSubscription(context.Background(), RemoveSubscriptionRequest{SubscriptionID: "sub_a"})

	resp, _ := idx.LookupSubscription(context.Background(), LookupSubscriptionRequest{SubscriptionID: "sub_a"})
	assert.False(t, resp.Found)
	assert.Nil(t, resp.Deliver)
}

func TestInMemorySubscriptionIndex_RemoveUnknownIsNoOp(t *testing.T) {
	idx := NewInMemorySubscriptionIndex()
	_, err := idx.RemoveSubscription(context.Background(), RemoveSubscriptionRequest{SubscriptionID: "nope"})
	require.NoError(t, err)

	count, _ := idx.CountSubscriptions(context.Background(), CountSubscriptionsRequest{})
	assert.Equal(t, 0, count.Count)
}

func TestInMemorySubscriptionIndex_LookupMissReturnsZeroValues(t *testing.T) {
	idx := NewInMemorySubscriptionIndex()
	resp, err := idx.LookupSubscription(context.Background(), LookupSubscriptionRequest{SubscriptionID: "nope"})
	require.NoError(t, err)
	assert.False(t, resp.Found)
	assert.Nil(t, resp.Deliver)
	assert.Equal(t, deliveryModeUnset, resp.Mode, "lookup miss returns the zero DeliveryMode (unset)")
}

func TestInMemorySubscriptionIndex_CountTracksAddsAndRemoves(t *testing.T) {
	idx := NewInMemorySubscriptionIndex()
	ctx := context.Background()

	zero, _ := idx.CountSubscriptions(ctx, CountSubscriptionsRequest{})
	assert.Equal(t, 0, zero.Count)

	_, _ = idx.AddSubscription(ctx, AddSubscriptionRequest{SubscriptionID: "a", Mode: DeliveryModePush, Deliver: func(Event) {}})
	_, _ = idx.AddSubscription(ctx, AddSubscriptionRequest{SubscriptionID: "b", Mode: DeliveryModePush, Deliver: func(Event) {}})
	two, _ := idx.CountSubscriptions(ctx, CountSubscriptionsRequest{})
	assert.Equal(t, 2, two.Count)

	_, _ = idx.RemoveSubscription(ctx, RemoveSubscriptionRequest{SubscriptionID: "a"})
	one, _ := idx.CountSubscriptions(ctx, CountSubscriptionsRequest{})
	assert.Equal(t, 1, one.Count)
}

func TestInMemorySubscriptionIndex_AddEmptySubIDIsDropped(t *testing.T) {
	// Preserves the legacy defensive behavior the old Add had.
	idx := NewInMemorySubscriptionIndex()
	_, _ = idx.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: "", Mode: DeliveryModePush, Deliver: func(Event) {},
	})
	count, _ := idx.CountSubscriptions(context.Background(), CountSubscriptionsRequest{})
	assert.Equal(t, 0, count.Count, "empty SubscriptionID must be silently dropped")
}

func TestInMemorySubscriptionIndex_AddNilDeliverIsDropped(t *testing.T) {
	idx := NewInMemorySubscriptionIndex()
	_, _ = idx.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: "sub_a", Mode: DeliveryModePush, Deliver: nil,
	})
	count, _ := idx.CountSubscriptions(context.Background(), CountSubscriptionsRequest{})
	assert.Equal(t, 0, count.Count, "nil Deliver must be silently dropped")
}

// spySubscriptionIndex wraps an in-memory index with a call recorder so
// EmitToSubscription's interface-routing path is verifiable end-to-end.
type spySubscriptionIndex struct {
	inner SubscriptionIndexStore
	mu    sync.Mutex
	calls []string
}

func newSpySubscriptionIndex() *spySubscriptionIndex {
	return &spySubscriptionIndex{inner: NewInMemorySubscriptionIndex()}
}

func (s *spySubscriptionIndex) record(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, op)
}

func (s *spySubscriptionIndex) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *spySubscriptionIndex) AddSubscription(ctx context.Context, req AddSubscriptionRequest) (AddSubscriptionResponse, error) {
	s.record("Add")
	return s.inner.AddSubscription(ctx, req)
}
func (s *spySubscriptionIndex) RemoveSubscription(ctx context.Context, req RemoveSubscriptionRequest) (RemoveSubscriptionResponse, error) {
	s.record("Remove")
	return s.inner.RemoveSubscription(ctx, req)
}
func (s *spySubscriptionIndex) LookupSubscription(ctx context.Context, req LookupSubscriptionRequest) (LookupSubscriptionResponse, error) {
	s.record("Lookup")
	return s.inner.LookupSubscription(ctx, req)
}
func (s *spySubscriptionIndex) CountSubscriptions(ctx context.Context, req CountSubscriptionsRequest) (CountSubscriptionsResponse, error) {
	s.record("Count")
	return s.inner.CountSubscriptions(ctx, req)
}

func TestEmitToSubscription_RoutesViaInterface(t *testing.T) {
	spy := newSpySubscriptionIndex()
	var delivered Event
	_, _ = spy.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: "sub_a", Mode: DeliveryModePush,
		Deliver: func(e Event) { delivered = e },
	})

	EmitToSubscription(spy, Event{EventID: "evt_y"}, "sub_a")

	assert.Equal(t, []string{"Add", "Lookup"}, spy.snapshot())
	assert.Equal(t, "evt_y", delivered.EventID)
}

func TestEmitToSubscription_LookupMissDropsSilently(t *testing.T) {
	spy := newSpySubscriptionIndex()
	// No Add — Lookup will miss.
	EmitToSubscription(spy, Event{EventID: "evt_z"}, "unknown")
	assert.Equal(t, []string{"Lookup"}, spy.snapshot(), "miss must not panic; only one Lookup")
}

func TestLegacyAddRemoveLookupShimsStillWork(t *testing.T) {
	// Backward compat: the deprecated Add / Remove / Lookup / Len
	// methods still drive the underlying state via the new gRPC-style
	// methods.
	idx := NewSubscriptionIndex()
	idx.Add("sub_a", DeliveryModePush, func(Event) {})
	assert.Equal(t, 1, idx.Len())

	mode, deliver, ok := idx.Lookup("sub_a")
	assert.True(t, ok)
	assert.Equal(t, DeliveryModePush, mode)
	assert.NotNil(t, deliver)

	idx.Remove("sub_a")
	_, _, ok = idx.Lookup("sub_a")
	assert.False(t, ok)
}
