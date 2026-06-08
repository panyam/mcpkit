package events

import "context"

// QuotaStore is the storage seam behind Quota's per-(principal, eventName)
// reservation counts. The default in-memory implementation
// (NewInMemoryQuotaStore) matches the historical behavior exactly;
// alternative implementations plug in via WithQuotaStore.
//
// Cap configuration stays on the Quota wrapper — caps are static
// operator config set at construction. The store owns dynamic
// reservation state only: how many slots are currently held for each
// (Principal, EventName). The wrapper passes Max on every
// ReserveQuota call, so the store's atomicity unit is "compare current
// count against Max and conditionally increment" — a one-shot
// compare-and-set that maps cleanly onto Postgres `ON CONFLICT DO
// UPDATE … WHERE count < $max` and Redis `EVAL` scripts.
//
// API shape: every method follows the gRPC-style
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// convention pinned in STORAGE_SEAMS.md. ctx threads cancellation,
// deadlines, and trace context. Application-level state (Granted,
// Count) lives on the response — error is reserved for storage-layer
// failures (connection drops, transaction conflicts).
//
// Concurrency contract: Quota calls these methods under its own
// mutex today, so the in-memory implementation does NOT take internal
// locks. Implementations that share state across multiple Quota
// instances (Postgres, Redis) take their own locks or use transaction
// semantics — the wrapper's local mutex is not enough at that scale.
type QuotaStore interface {
	ReserveQuota(ctx context.Context, req ReserveQuotaRequest) (ReserveQuotaResponse, error)
	ReleaseQuota(ctx context.Context, req ReleaseQuotaRequest) (ReleaseQuotaResponse, error)
	CountQuota(ctx context.Context, req CountQuotaRequest) (CountQuotaResponse, error)
}

// ReserveQuotaRequest asks the store to claim one slot for
// (Principal, EventName) only if the current count is strictly less
// than Max. Implementations MUST perform the check + increment
// atomically with respect to concurrent Reserve / Release calls for
// the same key.
type ReserveQuotaRequest struct {
	Principal string
	EventName string
	// Max is the cap the wrapper read from its caps map. Must be > 0;
	// the wrapper short-circuits for uncapped event types and never
	// calls the store with Max <= 0.
	Max int
}

// ReserveQuotaResponse carries the reservation outcome. Granted is
// true iff the slot was claimed; Count is the current count after
// reservation (when Granted) or the count that blocked reservation
// (when not).
type ReserveQuotaResponse struct {
	Granted bool
	Count   int
}

// ReleaseQuotaRequest returns one slot for (Principal, EventName).
// The store decrements the count if it is currently > 0, otherwise
// no-ops (silent — release-at-zero is not an error). Pair with
// ReserveQuota 1:1 from the wrapper's call sites; double-Release is
// treated as a benign no-op rather than an underflow.
type ReleaseQuotaRequest struct {
	Principal string
	EventName string
}

// ReleaseQuotaResponse is empty today; reserved for future fields.
type ReleaseQuotaResponse struct{}

// CountQuotaRequest reads the current count for (Principal, EventName)
// without mutating state. Used by inspection paths (admin frontends,
// debugging); not on the Reserve / Release hot path.
type CountQuotaRequest struct {
	Principal string
	EventName string
}

// CountQuotaResponse carries the current count. Implementations MAY
// approximate Count for performance (returning a value off by a few
// when contention is high) — callers use it for inspection, never for
// correctness-critical comparisons.
type CountQuotaResponse struct {
	Count int
}

// NewInMemoryQuotaStore returns the default in-memory storage
// implementation — a plain map[quotaStoreKey]int with no internal
// locking. Suitable for single-process deployments and the default
// when WithQuotaStore is not configured. Multi-replica deployments
// plug in a shared backend (e.g., the Postgres / Redis implementations
// in #630 / #634).
func NewInMemoryQuotaStore() QuotaStore {
	return &inMemoryQuotaStore{counts: make(map[quotaStoreKey]int)}
}

// WithQuotaStore overrides the wrapper's storage implementation.
// Passing nil is treated as "use the default in-memory store" — the
// constructor materializes a fresh NewInMemoryQuotaStore in that case.
// Explicit configuration is recommended for clarity at Quota
// construction sites.
func WithQuotaStore(s QuotaStore) QuotaOption {
	return func(q *Quota) {
		if s != nil {
			q.store = s
		}
	}
}

// quotaStoreKey is the in-memory store's composite key. Distinct from
// the historical quotaKey type so the seam refactor doesn't accidentally
// hand out the old type to external callers.
type quotaStoreKey struct {
	principal string
	eventName string
}

// inMemoryQuotaStore is the default QuotaStore implementation.
// Unlocked because Quota calls every method under its own mutex —
// the store sees a single goroutine at a time per wrapper instance.
type inMemoryQuotaStore struct {
	counts map[quotaStoreKey]int
}

func (s *inMemoryQuotaStore) ReserveQuota(_ context.Context, req ReserveQuotaRequest) (ReserveQuotaResponse, error) {
	k := quotaStoreKey{principal: req.Principal, eventName: req.EventName}
	cur := s.counts[k]
	if cur >= req.Max {
		return ReserveQuotaResponse{Granted: false, Count: cur}, nil
	}
	s.counts[k] = cur + 1
	return ReserveQuotaResponse{Granted: true, Count: cur + 1}, nil
}

func (s *inMemoryQuotaStore) ReleaseQuota(_ context.Context, req ReleaseQuotaRequest) (ReleaseQuotaResponse, error) {
	k := quotaStoreKey{principal: req.Principal, eventName: req.EventName}
	if s.counts[k] > 0 {
		s.counts[k]--
	}
	return ReleaseQuotaResponse{}, nil
}

func (s *inMemoryQuotaStore) CountQuota(_ context.Context, req CountQuotaRequest) (CountQuotaResponse, error) {
	k := quotaStoreKey{principal: req.Principal, eventName: req.EventName}
	return CountQuotaResponse{Count: s.counts[k]}, nil
}
