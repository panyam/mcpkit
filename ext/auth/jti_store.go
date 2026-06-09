package auth

import (
	"context"
	"sync"
	"time"
)

// JTIStore is the storage seam behind logout_token replay protection.
// OIDC Back-Channel Logout 1.0 § 2.6 says the receiver "MUST verify
// that no `jti` claim in the [Logout] Token has been seen before".
// Implementations must therefore retain seen jtis at least as long as
// the spec-allowed replay window (the spec is silent on a concrete
// window; we default to twice the max permitted clock skew, which is
// the same minimum-floor every BCL impl seems to converge on).
//
// gRPC-style request/response signatures so the in-memory default in
// this file can be swapped for a Redis / SQL / shared-cache impl
// without changing call sites. Forward-compatible with a future
// remote JTIStore that wraps a gRPC client — ctx threads cancellation
// and trace context to the storage backend.
type JTIStore interface {
	// Seen returns whether the jti has been recorded within its TTL
	// window. Used by the BCL receiver to reject replays. Spec §2.6:
	// "If the Logout Token has been seen before, [the receiver] MUST
	// reject the request as a replay."
	Seen(ctx context.Context, req SeenRequest) (SeenResponse, error)

	// Record stores a jti with the given TTL. Re-recording an existing
	// jti is allowed and resets its TTL — the spec does not require
	// monotonic-only recording, and forcing it would complicate the
	// in-memory impl for no observable gain.
	Record(ctx context.Context, req RecordRequest) (RecordResponse, error)
}

// SeenRequest carries the jti being checked. JTI is the bare
// `jti` claim value from the logout_token — callers do not need
// to prefix or namespace it; the store treats the value as opaque.
type SeenRequest struct {
	JTI string
}

// SeenResponse reports whether the jti has been recorded within
// its TTL. Found=false is the success path (jti is fresh, proceed
// to Record). Found=true means the BCL handler MUST respond 400.
type SeenResponse struct {
	Found bool
}

// RecordRequest carries the jti + TTL to persist. TTL is the
// duration the jti must remain visible to Seen() — typically set
// from the BCL handler's configured replay window, NOT from the
// logout_token's `exp` claim (because a malicious AS could shrink
// exp and turn a replay window into nothing).
type RecordRequest struct {
	JTI string
	TTL time.Duration
}

// RecordResponse is a marker; success is err=nil. No Found-style
// field because the spec doesn't require any signal on the record
// path (overwriting an existing jti is fine — the handler already
// rejected the replay via Seen()).
type RecordResponse struct{}

// MemoryJTIStore is the default in-memory JTIStore implementation —
// a map of jti → expiry, swept lazily on each Seen / Record call.
// Suitable for single-process deployments and tests. Multi-replica
// setups should swap for a Redis / shared-cache impl so a replay
// caught on replica A is also rejected on replica B.
//
// Thread-safe via an internal RWMutex. Lazy sweep keeps memory
// bounded to the rate of unique jtis arriving during the active
// TTL window — there is no background goroutine, which keeps the
// store lifecycle equal to the receiver's lifecycle (no shutdown
// dance required).
type MemoryJTIStore struct {
	mu      sync.RWMutex
	entries map[string]time.Time
	now     func() time.Time
}

// NewMemoryJTIStore constructs a fresh in-memory JTIStore.
func NewMemoryJTIStore() *MemoryJTIStore {
	return &MemoryJTIStore{
		entries: make(map[string]time.Time),
		now:     time.Now,
	}
}

// Seen reports whether the jti exists and is unexpired. Lazily
// evicts an entry that is past its TTL so the next Record call for
// the same jti starts a fresh window.
func (s *MemoryJTIStore) Seen(_ context.Context, req SeenRequest) (SeenResponse, error) {
	if req.JTI == "" {
		return SeenResponse{}, nil
	}
	s.mu.RLock()
	exp, ok := s.entries[req.JTI]
	s.mu.RUnlock()
	if !ok {
		return SeenResponse{Found: false}, nil
	}
	if s.now().After(exp) {
		// Past TTL — evict and report as not seen.
		s.mu.Lock()
		// Double-check under write lock so a concurrent Record
		// that re-extended the TTL is preserved.
		if exp2, stillThere := s.entries[req.JTI]; stillThere && s.now().After(exp2) {
			delete(s.entries, req.JTI)
		}
		s.mu.Unlock()
		return SeenResponse{Found: false}, nil
	}
	return SeenResponse{Found: true}, nil
}

// Record persists the jti with the given TTL. Re-recording is
// allowed; the new TTL replaces the old one.
func (s *MemoryJTIStore) Record(_ context.Context, req RecordRequest) (RecordResponse, error) {
	if req.JTI == "" || req.TTL <= 0 {
		return RecordResponse{}, nil
	}
	s.mu.Lock()
	s.entries[req.JTI] = s.now().Add(req.TTL)
	s.mu.Unlock()
	return RecordResponse{}, nil
}
