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
// HandleStore[T] is the interface tool authors program against. The
// default constructor NewHandleStore returns an in-memory implementation
// (InMemoryHandleStore[T]); persistent backends — Redis etc. — satisfy
// the same interface from their own constructors. Tracked separately:
// see panyam/mcpkit#471 (Redis), #472 (admin endpoints).
//
// SEP-2567 itself ships no upstream conformance suite — this is
// pattern-support, not a tested wire surface.

// HandleStore is the typed store for SEP-2567 state handles. One store
// per logical type (cart, document, transaction, ...) keeps the type
// assertions in tool handlers clean. A single Server may hold any
// number of HandleStores side by side; they share no state.
//
// All methods are safe for concurrent use.
type HandleStore[T any] interface {
	// Mint stores v under a freshly-minted opaque id and returns the
	// id. ttl=0 falls back to the store's default TTL (if any); ttl<0
	// forces "never expires" regardless of any default; ttl>0 pins a
	// per-handle override.
	Mint(v T, ttl time.Duration) string

	// Get returns the value stored under id, or zero+false if no such
	// handle is registered OR if the handle has expired. Lazy expiry —
	// implementations are expected to surface ok=false past TTL even
	// without a background sweep.
	Get(id string) (T, bool)

	// Delete removes the entry under id and returns whether it was
	// present. Safe to call with an unknown id (no-op).
	Delete(id string) bool

	// Len returns the current entry count (may include expired-but-
	// not-swept entries depending on the implementation). Primarily
	// for tests and ops introspection.
	Len() int

	// Close releases any background resources (GC goroutines, network
	// connections to backends). Always safe to call; idempotent.
	Close()
}

// HandleStoreOption configures an InMemoryHandleStore at construction.
// Backend-specific implementations may accept their own option types.
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

// NewHandleStore returns the default in-memory implementation. Most
// callers should use this; integrators who need cross-replica sharing
// construct a backend-specific store directly (which returns the same
// HandleStore[T] interface).
//
// Pass WithHandleGCInterval to enable background expiry sweeps; without
// it the store relies on lazy expiry (Get checks the timestamp).
func NewHandleStore[T any](opts ...HandleStoreOption) HandleStore[T] {
	return NewInMemoryHandleStore[T](opts...)
}

// InMemoryHandleStore is the default HandleStore implementation. Holds
// entries in a Go map under an RWMutex. No persistence across process
// restarts — for cross-replica deployments use a backend-backed
// implementation (tracked by panyam/mcpkit#471).
//
// Exported so tests and code that needs the concrete type (e.g. for
// custom introspection beyond Len) can construct it directly. New code
// should prefer NewHandleStore's interface return.
type InMemoryHandleStore[T any] struct {
	mu      sync.RWMutex
	entries map[string]handleEntry[T]

	defaultTTL time.Duration
	idPrefix   string

	gcInterval time.Duration
	gcStop     chan struct{}
	gcDone     chan struct{}
}

// handleEntry holds one stored value with its expiry.
type handleEntry[T any] struct {
	value     T
	expiresAt time.Time // zero = never expires
}

// Compile-time check that InMemoryHandleStore satisfies the interface
// for an arbitrary type. The struct{}-instance is throwaway; the
// assertion only catches drift between the interface and the impl.
var _ HandleStore[struct{}] = (*InMemoryHandleStore[struct{}])(nil)

// NewInMemoryHandleStore constructs the in-memory implementation
// directly, returning the concrete struct for callers that need it.
// Most call sites should prefer NewHandleStore (returns interface).
func NewInMemoryHandleStore[T any](opts ...HandleStoreOption) *InMemoryHandleStore[T] {
	o := handleStoreOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	s := &InMemoryHandleStore[T]{
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
// prefixed via WithHandleIDPrefix.
func (s *InMemoryHandleStore[T]) Mint(v T, ttl time.Duration) string {
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
func (s *InMemoryHandleStore[T]) Get(id string) (T, bool) {
	s.mu.RLock()
	entry, ok := s.entries[id]
	s.mu.RUnlock()
	if !ok {
		var zero T
		return zero, false
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		// Lazy delete on read of an expired entry so the next gc tick
		// has less to do.
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
func (s *InMemoryHandleStore[T]) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return false
	}
	delete(s.entries, id)
	return true
}

// Len returns the current entry count (including expired-but-not-swept).
func (s *InMemoryHandleStore[T]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Close stops the background gc goroutine, if any. Idempotent.
func (s *InMemoryHandleStore[T]) Close() {
	if s.gcStop == nil {
		return
	}
	select {
	case <-s.gcStop:
		return
	default:
	}
	close(s.gcStop)
	<-s.gcDone
	s.gcStop = nil
}

// gcLoop is the background expiry sweeper. Exits on Close.
func (s *InMemoryHandleStore[T]) gcLoop() {
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
func (s *InMemoryHandleStore[T]) sweepExpired() {
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
func (s *InMemoryHandleStore[T]) newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
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
