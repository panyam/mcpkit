// Package stores holds mcpkit's generic storage seams — backend-agnostic
// interfaces (plus in-memory defaults) that library features call through so a
// deployment can swap in Redis, SQL, or a custom backend. See STORAGE_SEAMS.md.
package stores

import "context"

// QuotaStore is the storage seam behind a per-(Principal, Key) reservation
// counter. The shape is generic: Principal identifies who is reserving, Key
// names the thing being rate-limited (an event type, a tool name, a method —
// whatever the caller buckets on). The default in-memory implementation
// (NewInMemoryQuotaStore) keeps counts in a plain map; alternative
// implementations (Redis, SQL) plug in behind the same interface.
//
// API shape follows the gRPC-style convention pinned in STORAGE_SEAMS.md:
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// ctx threads cancellation, deadlines, and trace context. Application-level
// state (Granted, Count) lives on the response — error is reserved for
// storage-layer failures (connection drops, transaction conflicts).
//
// Concurrency contract: the atomicity unit is "compare current count against
// Max and conditionally increment" — a one-shot compare-and-set that maps
// cleanly onto Postgres `ON CONFLICT DO UPDATE … WHERE count < $max` and Redis
// `EVAL` scripts. The in-memory implementation does NOT take internal locks;
// it assumes the caller serializes access (the events Quota wrapper calls under
// its own mutex). Implementations shared across callers must take their own
// locks or use transaction semantics.
type QuotaStore interface {
	ReserveQuota(ctx context.Context, req ReserveQuotaRequest) (ReserveQuotaResponse, error)
	ReleaseQuota(ctx context.Context, req ReleaseQuotaRequest) (ReleaseQuotaResponse, error)
	CountQuota(ctx context.Context, req CountQuotaRequest) (CountQuotaResponse, error)
}

// ReserveQuotaRequest asks the store to claim one slot for (Principal, Key)
// only if the current count is strictly less than Max. Implementations MUST
// perform the check + increment atomically with respect to concurrent
// Reserve / Release calls for the same key.
type ReserveQuotaRequest struct {
	Principal string
	// Key names the bucket being rate-limited (event type, tool, method, ...).
	Key string
	// Max is the cap the caller read from its config. Must be > 0; callers
	// short-circuit uncapped keys and never call the store with Max <= 0.
	Max int
}

// ReserveQuotaResponse carries the reservation outcome. Granted is true iff the
// slot was claimed; Count is the current count after reservation (when Granted)
// or the count that blocked reservation (when not).
type ReserveQuotaResponse struct {
	Granted bool
	Count   int
}

// ReleaseQuotaRequest returns one slot for (Principal, Key). The store
// decrements the count if it is currently > 0, otherwise no-ops (release-at-zero
// is a benign no-op, not an error). Pair 1:1 with ReserveQuota.
type ReleaseQuotaRequest struct {
	Principal string
	Key       string
}

// ReleaseQuotaResponse is empty today; reserved for future fields.
type ReleaseQuotaResponse struct{}

// CountQuotaRequest reads the current count for (Principal, Key) without
// mutating state. Used by inspection paths (admin frontends, debugging); not on
// the Reserve / Release hot path.
type CountQuotaRequest struct {
	Principal string
	Key       string
}

// CountQuotaResponse carries the current count. Implementations MAY approximate
// Count for performance under high contention — callers use it for inspection,
// never for correctness-critical comparisons.
type CountQuotaResponse struct {
	Count int
}

// NewInMemoryQuotaStore returns the default in-memory storage implementation —
// a plain map with no internal locking. Suitable for single-process
// deployments and the default when no store is configured. Multi-replica
// deployments plug in a shared backend.
func NewInMemoryQuotaStore() QuotaStore {
	return &inMemoryQuotaStore{counts: make(map[quotaStoreKey]int)}
}

// quotaStoreKey is the in-memory store's composite key.
type quotaStoreKey struct {
	principal string
	key       string
}

// inMemoryQuotaStore is the default QuotaStore implementation. Unlocked because
// callers serialize access per store instance.
type inMemoryQuotaStore struct {
	counts map[quotaStoreKey]int
}

func (s *inMemoryQuotaStore) ReserveQuota(_ context.Context, req ReserveQuotaRequest) (ReserveQuotaResponse, error) {
	k := quotaStoreKey{principal: req.Principal, key: req.Key}
	cur := s.counts[k]
	if cur >= req.Max {
		return ReserveQuotaResponse{Granted: false, Count: cur}, nil
	}
	s.counts[k] = cur + 1
	return ReserveQuotaResponse{Granted: true, Count: cur + 1}, nil
}

func (s *inMemoryQuotaStore) ReleaseQuota(_ context.Context, req ReleaseQuotaRequest) (ReleaseQuotaResponse, error) {
	k := quotaStoreKey{principal: req.Principal, key: req.Key}
	if s.counts[k] > 0 {
		s.counts[k]--
	}
	return ReleaseQuotaResponse{}, nil
}

func (s *inMemoryQuotaStore) CountQuota(_ context.Context, req CountQuotaRequest) (CountQuotaResponse, error) {
	k := quotaStoreKey{principal: req.Principal, key: req.Key}
	return CountQuotaResponse{Count: s.counts[k]}, nil
}
