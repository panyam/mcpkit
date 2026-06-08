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
// Reservation-count state lives behind the QuotaStore seam — see
// quota_store.go. The default in-memory implementation matches the
// historical behavior; multi-replica deployments plug in Postgres
// (#630) or Redis (#634) for cross-replica counter atomicity.

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrTooManySubscriptions is returned by Quota.Reserve when the cap
// for (principal, event-type) is exceeded. Surfaces on the wire as
// -32013 ResourceExhausted with data.limit="subscriptions" per the
// MCP Events spec's reusable error-code set.
//
// Use errors.Is(err, ErrTooManySubscriptions) to test; the wrapping
// error includes the event-type name and cap in its message. Use
// Quota.Cap(eventName) to read the cap as a typed value (for the
// ResourceExhausted data.max field) without parsing the message.
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
// The wrapper owns the caps map (static operator config) and routes
// dynamic reservation state through a QuotaStore (see quota_store.go).
// The default in-memory store has no internal locking — the wrapper's
// mu serializes calls. Multi-replica deployments plug in a shared
// backend via WithQuotaStore.
type Quota struct {
	mu    sync.Mutex
	store QuotaStore
	caps  map[string]int
}

// NewQuota constructs a quota with the given options. With no
// WithMaxSubscriptionsPerPrincipal options, every Reserve call
// succeeds — the quota is effectively a no-op. Authors register
// caps explicitly per event-type. The default reservation-count
// store is in-memory; override via WithQuotaStore.
func NewQuota(opts ...QuotaOption) *Quota {
	q := &Quota{
		store: NewInMemoryQuotaStore(),
		caps:  make(map[string]int),
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Reserve attempts to claim one subscription slot for (principal,
// eventName). Returns nil on success. Returns a wrapped
// ErrTooManySubscriptions when the cap is exceeded.
//
// Event types without a configured cap always succeed (no store call
// is made — keeps the store from accumulating state for uncapped
// event-types).
func (q *Quota) Reserve(principal, eventName string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	cap, ok := q.caps[eventName]
	if !ok || cap <= 0 {
		return nil
	}
	resp, _ := q.store.ReserveQuota(context.Background(), ReserveQuotaRequest{
		Principal: principal,
		EventName: eventName,
		Max:       cap,
	})
	if !resp.Granted {
		return fmt.Errorf("%w: principal %q at cap %d for %q",
			ErrTooManySubscriptions, principal, cap, eventName)
	}
	return nil
}

// Release returns one subscription slot for (principal, eventName).
//
// Pair with Reserve 1:1 from the calling site. Release-at-zero is a
// silent no-op (the store-seam contract), so double-Release does not
// panic — that's a more permissive semantics than the previous
// semaphore-backed implementation, but matches Postgres /
// Redis-backed implementations where the underlying ops are
// idempotent. The SDK's wiring pairs Reserve/Release by construction:
//
//   - Webhook: Reserve in registerSubscribe after isNew; Release in
//     the registry's onRemove hook (one fire per actual removal).
//   - Push: Reserve before Subscribe; defer Release.
//   - Poll: Reserve after Touch returns isNew; Release in the lease
//     table's chained onExpire.
//
// Event types without a configured cap are no-ops (no store call is
// made).
func (q *Quota) Release(principal, eventName string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cap, ok := q.caps[eventName]
	if !ok || cap <= 0 {
		return
	}
	_, _ = q.store.ReleaseQuota(context.Background(), ReleaseQuotaRequest{
		Principal: principal,
		EventName: eventName,
	})
}

// Cap returns the configured per-principal cap for eventName, or 0 if
// the event-type is uncapped. Wire-format helper: emission sites use
// the returned value to populate ResourceExhaustedData.Max on -32013
// responses without parsing the wrapped error's message.
func (q *Quota) Cap(eventName string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.caps[eventName]
}
