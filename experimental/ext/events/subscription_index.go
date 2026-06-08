package events

import (
	"context"
	"sync"
)

// SubscriptionIndexStore is the storage seam behind subscription
// routing. The default in-memory implementation — the SubscriptionIndex
// struct in this file — matches the historical behavior exactly;
// alternative implementations plug in for multi-replica deployments
// where the cross-replica fanout layer (issue 629) handles non-local
// subscriptions.
//
// Spec §"Server SDK Guidance" L630 names the use case: targeted
// delivery via EmitToSubscription depends on a fast subID → destination
// lookup. The index is maintained by the lifecycle wiring in
// events.Register — per-mode registration sites call AddSubscription
// when a subscription becomes live, RemoveSubscription when it tears
// down.
//
// API shape: every method follows the gRPC-style
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// convention pinned in STORAGE_SEAMS.md. ctx threads cancellation,
// deadlines, and trace context. Application-level absence is reported
// via Found on LookupSubscriptionResponse; error is reserved for
// storage-layer failures.
//
// Closure non-serializability: AddSubscriptionRequest.Deliver is a
// per-replica closure that captures local state (http.Client, push
// channel). It cannot cross a network. Remote implementations of
// SubscriptionIndexStore handle non-local subscriptions through a
// separate cross-replica forwarding path (the EventBus seam, issue
// 629), not by passing closures over the wire.
//
// Concurrency contract: the in-memory implementation takes its own
// RWMutex (different from the WebhookStore and QuotaStore pattern,
// where the wrapper holds a mutex around store calls — here, there
// is no wrapper, the index is consumed directly). Implementations
// shared across replicas (Redis, Kafka topic listing) take their own
// locks or use transaction semantics.
type SubscriptionIndexStore interface {
	AddSubscription(ctx context.Context, req AddSubscriptionRequest) (AddSubscriptionResponse, error)
	RemoveSubscription(ctx context.Context, req RemoveSubscriptionRequest) (RemoveSubscriptionResponse, error)
	LookupSubscription(ctx context.Context, req LookupSubscriptionRequest) (LookupSubscriptionResponse, error)
	CountSubscriptions(ctx context.Context, req CountSubscriptionsRequest) (CountSubscriptionsResponse, error)
}

// AddSubscriptionRequest registers a subscription. Replaces any
// existing entry for the same SubscriptionID — webhook
// subscribe-then-refresh on the same canonical tuple keeps the same
// id but may have re-targeted; the latest wiring wins.
type AddSubscriptionRequest struct {
	SubscriptionID string
	Mode           DeliveryMode
	// Deliver is the local delivery closure. Captures per-replica
	// state — fundamentally non-serializable. Remote implementations
	// reject Add requests with a non-nil Deliver (returning an error)
	// and route non-local subscriptions through the cross-replica
	// EventBus seam instead.
	Deliver func(Event)
}

// AddSubscriptionResponse is empty today; reserved for future fields.
type AddSubscriptionResponse struct{}

// RemoveSubscriptionRequest drops the entry for SubscriptionID. Idempotent
// — removing an unknown id is a silent no-op.
type RemoveSubscriptionRequest struct {
	SubscriptionID string
}

// RemoveSubscriptionResponse is empty today.
type RemoveSubscriptionResponse struct{}

// LookupSubscriptionRequest fetches the routing entry for SubscriptionID.
type LookupSubscriptionRequest struct {
	SubscriptionID string
}

// LookupSubscriptionResponse carries the routing entry. Found is
// false when the subscription is unknown — caller should treat as
// "subscription was torn down" and drop the event with a debug log
// (the canonical EmitToSubscription path does this).
//
// Mode is the delivery mode the subscription was registered with;
// caller may shape the forwarded payload accordingly. Deliver is the
// per-replica delivery closure for local subscriptions; nil when the
// subscription is registered remotely (the cross-replica forwarder
// reconstructs delivery without a closure).
type LookupSubscriptionResponse struct {
	Mode    DeliveryMode
	Deliver func(Event)
	Found   bool
}

// CountSubscriptionsRequest is empty today.
type CountSubscriptionsRequest struct{}

// CountSubscriptionsResponse carries the current entry count.
// Implementations MAY approximate Count for performance.
type CountSubscriptionsResponse struct {
	Count int
}

// NewInMemorySubscriptionIndex returns the default in-memory storage
// implementation typed as the SubscriptionIndexStore interface.
// Matches the family pattern (NewInMemoryWebhookStore /
// NewInMemoryQuotaStore). For backward compatibility, NewSubscriptionIndex
// continues to return the concrete *SubscriptionIndex struct, which
// also satisfies SubscriptionIndexStore.
func NewInMemorySubscriptionIndex() SubscriptionIndexStore {
	return NewSubscriptionIndex()
}

// SubscriptionIndex maps a server-derived subscription id to the
// callback that delivers an event to that specific subscription. It
// is the routing table consulted by EmitToSubscription (spec §"Server
// SDK Guidance" L630) so an author who knows the target sub id can
// bypass broadcast fanout.
//
// SubscriptionIndex is the default in-memory implementation of the
// SubscriptionIndexStore interface — the struct name is preserved
// for backward compatibility with existing callers that hold
// *SubscriptionIndex references. Construct via NewSubscriptionIndex
// or NewInMemorySubscriptionIndex (the latter returns the interface
// type, matching the family convention).
//
// Push subscriptions: each open events/stream gets its own random
// sub id — concurrent streams from the same principal/name/params
// are distinct entries in the index. Webhook subscriptions: the
// spec's derived id (deriveSubscriptionID over the canonical tuple,
// §"Subscription Identity" → "Derived id" L367) is reused — refresh
// keeps the same id, so the index entry also stays put. Poll
// subscriptions are NOT indexed (no sub id; the lease tuple is the
// routing identity per spec §"Server SDK Guidance" → "Unsubscribe
// timing by mode" L707).
type SubscriptionIndex struct {
	mu      sync.RWMutex
	entries map[string]subscriptionEntry
}

type subscriptionEntry struct {
	mode    DeliveryMode
	deliver func(Event)
}

// NewSubscriptionIndex constructs an empty index. Authors who want a
// shared reference handy for EmitToSubscription should construct one
// and pass it via Config.SubscriptionIndex; otherwise Register
// constructs a default and uses it internally.
func NewSubscriptionIndex() *SubscriptionIndex {
	return &SubscriptionIndex{entries: make(map[string]subscriptionEntry)}
}

// AddSubscription implements SubscriptionIndexStore. Empty
// SubscriptionID or nil Deliver are silently dropped — preserves the
// old Add's defensive behavior.
func (i *SubscriptionIndex) AddSubscription(_ context.Context, req AddSubscriptionRequest) (AddSubscriptionResponse, error) {
	if req.SubscriptionID == "" || req.Deliver == nil {
		return AddSubscriptionResponse{}, nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.entries[req.SubscriptionID] = subscriptionEntry{mode: req.Mode, deliver: req.Deliver}
	return AddSubscriptionResponse{}, nil
}

// RemoveSubscription implements SubscriptionIndexStore. Empty
// SubscriptionID is a no-op.
func (i *SubscriptionIndex) RemoveSubscription(_ context.Context, req RemoveSubscriptionRequest) (RemoveSubscriptionResponse, error) {
	if req.SubscriptionID == "" {
		return RemoveSubscriptionResponse{}, nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.entries, req.SubscriptionID)
	return RemoveSubscriptionResponse{}, nil
}

// LookupSubscription implements SubscriptionIndexStore.
func (i *SubscriptionIndex) LookupSubscription(_ context.Context, req LookupSubscriptionRequest) (LookupSubscriptionResponse, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.entries[req.SubscriptionID]
	if !ok {
		return LookupSubscriptionResponse{}, nil
	}
	return LookupSubscriptionResponse{Mode: e.mode, Deliver: e.deliver, Found: true}, nil
}

// CountSubscriptions implements SubscriptionIndexStore.
func (i *SubscriptionIndex) CountSubscriptions(_ context.Context, _ CountSubscriptionsRequest) (CountSubscriptionsResponse, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return CountSubscriptionsResponse{Count: len(i.entries)}, nil
}

// Add registers a subscription via the legacy positional API. Routes
// through AddSubscription.
//
// Deprecated: use AddSubscription(ctx, AddSubscriptionRequest{…}).
// Retained for backward compatibility with callers that constructed
// *SubscriptionIndex before the storage seam landed.
func (i *SubscriptionIndex) Add(subID string, mode DeliveryMode, deliver func(Event)) {
	_, _ = i.AddSubscription(context.Background(), AddSubscriptionRequest{
		SubscriptionID: subID,
		Mode:           mode,
		Deliver:        deliver,
	})
}

// Remove drops the entry via the legacy positional API.
//
// Deprecated: use RemoveSubscription(ctx, RemoveSubscriptionRequest{…}).
func (i *SubscriptionIndex) Remove(subID string) {
	_, _ = i.RemoveSubscription(context.Background(), RemoveSubscriptionRequest{
		SubscriptionID: subID,
	})
}

// Lookup returns the entry via the legacy positional API.
//
// Deprecated: use LookupSubscription(ctx, LookupSubscriptionRequest{…}).
func (i *SubscriptionIndex) Lookup(subID string) (mode DeliveryMode, deliver func(Event), ok bool) {
	resp, _ := i.LookupSubscription(context.Background(), LookupSubscriptionRequest{
		SubscriptionID: subID,
	})
	if !resp.Found {
		return deliveryModeUnset, nil, false
	}
	return resp.Mode, resp.Deliver, true
}

// Len returns the number of currently-indexed subscriptions via the
// legacy API.
//
// Deprecated: use CountSubscriptions(ctx, CountSubscriptionsRequest{}).
func (i *SubscriptionIndex) Len() int {
	resp, _ := i.CountSubscriptions(context.Background(), CountSubscriptionsRequest{})
	return resp.Count
}
