package events

import "sync"

// SubscriptionIndex maps a server-derived subscription id to the
// callback that delivers an event to that specific subscription. It
// is the routing table consulted by EmitToSubscription (η-5) so an
// author who knows the target sub id can bypass broadcast fanout.
//
// The index is maintained by the lifecycle wiring in Register:
// per-mode registration sites call Add when a subscription becomes
// live and Remove when it's torn down. Mode is recorded so callers
// that want to introspect ("is this a push or webhook subscription?")
// can do so without scanning the deliver closure.
//
// Push subscriptions: each open events/stream gets its own random
// sub id (η-3) — concurrent streams from the same principal/name/
// params are distinct entries in the index. Webhook subscriptions:
// the spec's derived id (deriveSubscriptionID over the canonical
// tuple) is reused — refresh keeps the same id, so the index entry
// also stays put. Poll subscriptions are NOT indexed (no sub id;
// the lease tuple is the routing identity per Q4).
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

// Add registers a subscription. Subsequent EmitToSubscription calls
// for subID will route to deliver. Replaces any existing entry for the
// same subID — webhook subscribe-then-refresh on the same canonical
// tuple keeps the same id but may have re-targeted; the latest wiring
// wins.
//
// deliver should be non-blocking and tolerate the subscription being
// torn down concurrently (Remove may race with delivery).
func (i *SubscriptionIndex) Add(subID string, mode DeliveryMode, deliver func(Event)) {
	if subID == "" || deliver == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.entries[subID] = subscriptionEntry{mode: mode, deliver: deliver}
}

// Remove drops the entry for subID. Safe to call for an unknown id
// (no-op).
func (i *SubscriptionIndex) Remove(subID string) {
	if subID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.entries, subID)
}

// Lookup returns the entry's mode and deliver function. ok is false
// when the id is unknown — caller should treat as "subscription was
// torn down" and drop the event with a debug log (the canonical
// EmitToSubscription path does this).
func (i *SubscriptionIndex) Lookup(subID string) (mode DeliveryMode, deliver func(Event), ok bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.entries[subID]
	if !ok {
		return deliveryModeUnset, nil, false
	}
	return e.mode, e.deliver, true
}

// Len returns the number of currently-indexed subscriptions. Snapshot
// — callers must not race on it for correctness.
func (i *SubscriptionIndex) Len() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.entries)
}
