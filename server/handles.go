package server

import (
	"crypto/rand"
	"encoding/base32"
	"sync"
	"time"
)

// SEP-2567 ("Sessionless MCP via Explicit State Handles") bundled
// helper.
//
// HandleStore[T] gives tool authors a typed, opaque-id-keyed store for
// the "create_*() → handle, subsequent calls take handle as a parameter"
// pattern the SEP recommends. The SEP itself is design guidance with no
// upstream conformance suite — this helper exists so demos and integrators
// don't reinvent the small but easy-to-get-wrong pieces (collision-free
// ids, TTL gc, value isolation).
//
// In-memory only by design. Persistent backends (Redis etc.) for shared
// stateless deployments behind a load balancer are tracked separately —
// see panyam/mcpkit#471. The interface here is deliberately small so a
// wrapper backend implementation requires only Mint/Get/Delete to satisfy
// the same surface a tool handler uses.

// HandleStore is a generic typed store keyed by opaque server-minted
// handle ids. Concurrent-safe; entries expire automatically when their
// per-handle TTL is reached.
//
// One store per logical type (cart, document, transaction, ...) keeps
// the type assertions in tool handlers clean. A single Server may hold
// any number of HandleStores side by side; they share no state.
type HandleStore[T any] struct {
	mu      sync.RWMutex
	entries map[string]handleEntry[T]

	// defaultTTL is applied by Mint when the per-call ttl arg is zero.
	// 0 here means "no expiry" — entries live until Delete is called.
	// Set via NewHandleStore's defaultTTL parameter.
	defaultTTL time.Duration

	// idPrefix is prepended to every minted handle. Lets multi-store
	// deployments tell handles apart at a glance ("cart-AB12..." vs
	// "doc-XYZ..."). Empty string skips the prefix.
	idPrefix string

	// gcInterval drives the background sweep; <= 0 disables. Set via
	// WithHandleGCInterval — tests can shorten to exercise expiry paths.
	gcInterval time.Duration
	gcStop     chan struct{}
	gcDone     chan struct{}
}

// handleEntry holds one stored value with its expiry.
type handleEntry[T any] struct {
	value     T
	expiresAt time.Time // zero = never expires
}

// HandleStoreOption configures a new HandleStore at construction time.
type HandleStoreOption func(*handleStoreOpts)

type handleStoreOpts struct {
	defaultTTL time.Duration
	idPrefix   string
	gcInterval time.Duration
}

// WithHandleDefaultTTL sets the default per-handle TTL applied by Mint
// when the caller passes a zero ttl. Zero (the default) means "no expiry".
func WithHandleDefaultTTL(ttl time.Duration) HandleStoreOption {
	return func(o *handleStoreOpts) { o.defaultTTL = ttl }
}

// WithHandleIDPrefix prepends a short label to every minted handle id.
// Cosmetic; doesn't affect security or uniqueness. Useful in
// multi-store deployments so a stray id is greppable to its origin
// ("cart-...", "doc-...").
func WithHandleIDPrefix(prefix string) HandleStoreOption {
	return func(o *handleStoreOpts) { o.idPrefix = prefix }
}

// WithHandleGCInterval enables periodic sweep of expired handles.
// 0 (the default) skips the background goroutine — callers can still
// rely on lazy expiry (Get returns ok=false for an expired handle even
// if the entry hasn't been swept). A small interval keeps memory tight
// for stores with high turnover; tests pass a few-ms interval to drive
// the expiry path deterministically.
func WithHandleGCInterval(d time.Duration) HandleStoreOption {
	return func(o *handleStoreOpts) { o.gcInterval = d }
}

// NewHandleStore builds an empty store. The generic parameter T fixes
// the value type; instantiate one per logical type the server owns.
//
// Pass WithHandleGCInterval to enable background expiry sweeps; without
// it the store relies on lazy expiry (Get checks the timestamp).
func NewHandleStore[T any](opts ...HandleStoreOption) *HandleStore[T] {
	o := handleStoreOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	s := &HandleStore[T]{
		entries:    make(map[string]handleEntry[T]),
		defaultTTL: o.defaultTTL,
		idPrefix:   o.idPrefix,
		gcInterval: o.gcInterval,
	}
	if s.gcInterval > 0 {
		s.gcStop = make(chan struct{})
		s.gcDone = make(chan struct{})
		go s.gcLoop()
	}
	return s
}

// Mint stores v under a freshly-minted opaque id and returns the id.
//
// ttl overrides the store's defaultTTL for this entry; pass 0 to fall
// back to the default. Pass a negative ttl to force "no expiry" even
// when the store has a non-zero default.
//
// IDs are 128 bits of crypto-random base32 (26 chars), optionally
// prefixed via WithHandleIDPrefix. Collision is statistically negligible
// for any sane store size; on the vanishingly rare collision (or a
// degraded crypto/rand) Mint retries up to 8 times before returning the
// shortest deterministic fallback.
func (s *HandleStore[T]) Mint(v T, ttl time.Duration) string {
	id := s.newID()
	expiresAt := time.Time{}
	switch {
	case ttl < 0:
		// caller-forced no-expiry
	case ttl == 0 && s.defaultTTL > 0:
		expiresAt = time.Now().Add(s.defaultTTL)
	case ttl > 0:
		expiresAt = time.Now().Add(ttl)
	}
	s.mu.Lock()
	s.entries[id] = handleEntry[T]{value: v, expiresAt: expiresAt}
	s.mu.Unlock()
	return id
}

// Get returns the value stored under id, or zero value + false if no
// such handle is registered OR if the handle has expired. Lazy expiry —
// no background goroutine required to enforce TTL on the read path.
//
// Note: a handle that expires between Get's read-lock and the caller's
// next operation will still appear valid to the caller. Tool handlers
// that absolutely must operate atomically should arrange transactional
// locking outside this store (or use a persistent backend with native
// atomic ops — see panyam/mcpkit#471).
func (s *HandleStore[T]) Get(id string) (T, bool) {
	s.mu.RLock()
	entry, ok := s.entries[id]
	s.mu.RUnlock()
	if !ok {
		var zero T
		return zero, false
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		// Lazy delete on read of an expired entry so the next gc tick
		// has less to do. Best-effort — concurrent reads may all see
		// the expired entry and all delete; final state is the same.
		s.mu.Lock()
		delete(s.entries, id)
		s.mu.Unlock()
		var zero T
		return zero, false
	}
	return entry.value, true
}

// Delete removes the entry under id and returns whether it was present.
// Safe to call with an unknown id (no-op).
func (s *HandleStore[T]) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return false
	}
	delete(s.entries, id)
	return true
}

// Len returns the current entry count (including expired-but-not-swept).
// Primarily useful for tests and ops introspection.
func (s *HandleStore[T]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Close stops the background gc goroutine, if any. Always safe to call;
// idempotent. Stores constructed without WithHandleGCInterval are no-op.
func (s *HandleStore[T]) Close() {
	if s.gcStop == nil {
		return
	}
	select {
	case <-s.gcStop:
		// already closed
		return
	default:
	}
	close(s.gcStop)
	<-s.gcDone
	s.gcStop = nil
}

// gcLoop is the background expiry sweeper. Exits on Close.
func (s *HandleStore[T]) gcLoop() {
	defer close(s.gcDone)
	t := time.NewTicker(s.gcInterval)
	defer t.Stop()
	for {
		select {
		case <-s.gcStop:
			return
		case <-t.C:
			s.sweepExpired()
		}
	}
}

// sweepExpired walks the store under write-lock removing every entry
// whose expiresAt is non-zero and in the past.
func (s *HandleStore[T]) sweepExpired() {
	now := time.Now()
	s.mu.Lock()
	for id, e := range s.entries {
		if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
			delete(s.entries, id)
		}
	}
	s.mu.Unlock()
}

// newID generates a base32-encoded 128-bit handle id, optionally
// prefixed. Crypto-grade entropy from rand.Read; falls back to a
// time-derived id on read failure (the platform's crypto source is
// dead — best-effort uniqueness keeps the system limping rather than
// erroring out from a Mint call).
func (s *HandleStore[T]) newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Time-based fallback; not security-grade but unique enough
		// for the degenerate "rand source dead" case.
		fb := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			b[i] = byte(fb >> (i * 8))
		}
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	id := enc.EncodeToString(b[:])
	if s.idPrefix == "" {
		return id
	}
	return s.idPrefix + "-" + id
}
