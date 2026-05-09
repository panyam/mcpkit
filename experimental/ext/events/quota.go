package events

// Per-principal-per-event-type subscription quota. Spec §"Server SDK
// Guidance" → "Subscription lifecycle hooks" L705: "Servers SHOULD
// enforce TooManySubscriptions before invoking on_subscribe."
//
// Why per-principal-per-event-type:
//
//   - Per event-type, total ("no more than 1000 subs to alert.fired")
//     limits the server's overall capacity for one event type but
//     doesn't protect against a single noisy principal.
//   - Per principal, total ("no more than 100 subs per user across all
//     event types") protects against runaway clients but limits
//     legitimate cross-event-type usage.
//   - Per principal, per event-type ("no more than 10 alert.fired
//     subs per user") is the natural anti-abuse axis: catches the
//     "one client spawning 10000 subs to one event" pattern without
//     getting in the way of normal cross-event-type usage.
//
// (per-principal, per-event-type) is the default; the other axes
// can be added behind options later if a deployment needs them.
//
// Implementation note: built on golang.org/x/sync/semaphore.Weighted,
// which is the canonical Go primitive for "at most N concurrent
// holders of a resource." Our bookkeeping is just per-(principal,
// event-name) instantiation of that primitive — we don't add our own
// counter math on top.

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/semaphore"
)

// ErrTooManySubscriptions is returned by Quota.Reserve when the cap
// for (principal, event-type) is exceeded. Surfaces on the wire as
// -32013 TooManySubscriptions per spec.
//
// Use errors.Is(err, ErrTooManySubscriptions) to test; the wrapping
// error includes the event-type name and cap in its message.
var ErrTooManySubscriptions = errors.New("TooManySubscriptions")

// QuotaOption configures a Quota at construction time.
type QuotaOption func(*Quota)

// WithMaxSubscriptionsPerPrincipal sets the cap on simultaneous
// subscriptions for one principal to one event-type. n <= 0 leaves
// the event-type uncapped (Reserve always succeeds). Multiple calls
// for the same eventName: last value wins.
//
// Authors that want a global default for all event types can set it
// per-EventDef when registering — there is no "all event types"
// option, deliberately, because every author cap so far has been
// per-event-type-specific.
func WithMaxSubscriptionsPerPrincipal(eventName string, n int) QuotaOption {
	return func(q *Quota) {
		if n > 0 && eventName != "" {
			q.caps[eventName] = n
		}
	}
}

// Quota tracks active subscription counts by (principal, eventName)
// and enforces per-event-type caps configured via
// WithMaxSubscriptionsPerPrincipal. Construct via NewQuota.
//
// Backed by golang.org/x/sync/semaphore.Weighted — one weighted
// semaphore per (principal, eventName) tuple, sized to that event's
// cap. Reserve = TryAcquire(1); Release = Release(1). Atomicity is
// the semaphore's job; we just route to the right one.
//
// All state is in-process; counts do not persist across restart.
// After restart a previously-suspended principal that was at cap
// will be allowed back up to the cap again on first re-subscribe.
type Quota struct {
	mu   sync.Mutex
	sems map[quotaKey]*semaphore.Weighted
	caps map[string]int
}

type quotaKey struct {
	principal string
	eventName string
}

// NewQuota constructs a quota with the given options. With no
// WithMaxSubscriptionsPerPrincipal options, every Reserve call
// succeeds — the quota is effectively a no-op. Authors register
// caps explicitly per event-type.
func NewQuota(opts ...QuotaOption) *Quota {
	q := &Quota{
		sems: make(map[quotaKey]*semaphore.Weighted),
		caps: make(map[string]int),
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// semFor returns the per-(principal, eventName) semaphore, lazily
// creating it on first use. Caller MUST hold q.mu.
func (q *Quota) semForLocked(principal, eventName string) *semaphore.Weighted {
	cap, ok := q.caps[eventName]
	if !ok {
		return nil
	}
	key := quotaKey{principal: principal, eventName: eventName}
	sem, exists := q.sems[key]
	if !exists {
		sem = semaphore.NewWeighted(int64(cap))
		q.sems[key] = sem
	}
	return sem
}

// Reserve attempts to claim one subscription slot for (principal,
// eventName). Returns nil on success. Returns a wrapped
// ErrTooManySubscriptions when the cap is exceeded.
//
// Event types without a configured cap always succeed (no semaphore
// is created — keeps the map from growing for uncapped event-types).
func (q *Quota) Reserve(principal, eventName string) error {
	q.mu.Lock()
	sem := q.semForLocked(principal, eventName)
	cap := q.caps[eventName]
	q.mu.Unlock()
	if sem == nil {
		return nil
	}
	if !sem.TryAcquire(1) {
		return fmt.Errorf("%w: principal %q at cap %d for %q",
			ErrTooManySubscriptions, principal, cap, eventName)
	}
	return nil
}

// Release returns one subscription slot for (principal, eventName).
//
// Pair with Reserve 1:1 from the calling site. Excess Release would
// panic (semaphore.Weighted.Release semantics). The SDK's wiring
// pairs them by construction:
//   - Webhook: Reserve in registerSubscribe after isNew; Release in
//     the registry's onRemove hook (one fire per actual removal).
//   - Push: Reserve before Subscribe; defer Release.
//   - Poll: Reserve after Touch returns isNew; Release in the lease
//     table's chained onExpire.
//
// Event types without a configured cap are no-ops (no semaphore
// existed).
func (q *Quota) Release(principal, eventName string) {
	q.mu.Lock()
	sem := q.sems[quotaKey{principal: principal, eventName: eventName}]
	q.mu.Unlock()
	if sem == nil {
		return
	}
	sem.Release(1)
}
