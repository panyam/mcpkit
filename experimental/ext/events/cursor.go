package events

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
)

// CursorProvider mints the monotone cursor for a yielded event. The
// cursor orders an event within its source: events/poll returns events
// whose cursor is strictly greater than a client's last-seen cursor, so
// for gap-free resume across a reconnect the values MUST be monotonically
// increasing and unique across every writer of that source.
//
// The default provider (InProcessCursors) is a per-source in-memory
// counter — correct and zero-overhead for a single writer, but each
// process starts at 1 and resets on restart, so N replicas writing the
// same source mint colliding cursors (issue 833). Swap in a provider that
// is monotone across writers when a source has more than one:
//
//   - Int64IncrCursors — a shared atomic-increment counter, e.g. a Redis
//     INCR (cross-replica, restart-safe).
//   - a cursor-providing EventBufferStore — see CursorProvidingStore,
//     where the store assigns the cursor from its own global sequence on
//     write, so mcpkit mints nothing.
//
// Wire a provider per source with WithCursorProvider. Precedence in
// YieldingSource: an explicitly-set provider wins; otherwise a
// cursor-providing store wins; otherwise InProcessCursors.
type CursorProvider interface {
	// Next returns the next cursor for source. Implementations MUST make
	// it monotone and unique across all concurrent writers of that
	// source. The returned string is compared numerically downstream, so
	// it must be a base-10 integer.
	Next(ctx context.Context, source string) (string, error)
}

// InProcessCursors is the default CursorProvider: a per-source in-memory
// atomic counter. It reproduces the historical YieldingSource behavior
// (each source counts 1, 2, 3, … independently) with zero dependencies.
//
// It is correct only for a single writer per source: the counter lives in
// one process and resets to 1 on restart, so two replicas writing the
// same source mint colliding cursors. Use Int64IncrCursors (a shared
// counter) or a cursor-providing store for multi-writer sources (issue
// 833).
type InProcessCursors struct {
	mu       sync.Mutex
	counters map[string]*atomic.Int64
}

// NewInProcessCursors returns an empty in-process cursor provider. A
// fresh one is installed as the default on every YieldingSource that does
// not set WithCursorProvider.
func NewInProcessCursors() *InProcessCursors {
	return &InProcessCursors{counters: make(map[string]*atomic.Int64)}
}

// Next returns the next per-source counter value as a base-10 string,
// starting at "1" for a source's first event.
func (p *InProcessCursors) Next(_ context.Context, source string) (string, error) {
	p.mu.Lock()
	c, ok := p.counters[source]
	if !ok {
		c = &atomic.Int64{}
		p.counters[source] = c
	}
	p.mu.Unlock()
	return strconv.FormatInt(c.Add(1), 10), nil
}

// Incrementer is the minimal contract Int64IncrCursors needs from a
// shared counter backend: an atomic increment of a named key that returns
// the new value. It is deliberately backend-neutral — a Redis INCR, a SQL
// sequence, an etcd counter, or any store with atomic increment satisfies
// it — so core events takes no dependency on any particular client.
// go-redis's (*redis.Client).Incr adapts to it in one line; a ready-made
// Redis adapter can live in stores/redis.
type Incrementer interface {
	Incr(ctx context.Context, key string) (int64, error)
}

// Int64IncrCursors is a CursorProvider backed by a shared atomic-increment
// counter (an Incrementer), one key per source. When the backing counter
// is shared across replicas (e.g. a Redis INCR), it is monotone and unique
// across every writer and survives process restarts — fixing the
// multi-writer / restart collision InProcessCursors has (issue 833)
// without requiring a cursor-providing store.
type Int64IncrCursors struct {
	incr   Incrementer
	prefix string
}

// DefaultCursorKeyPrefix is the key prefix Int64IncrCursors uses when none
// is supplied. The per-source key is prefix + source.
const DefaultCursorKeyPrefix = "events:cursor:"

// NewInt64IncrCursors returns a CursorProvider that mints cursors from the
// given Incrementer. keyPrefix is prepended to the source name to form the
// counter key; empty uses DefaultCursorKeyPrefix.
func NewInt64IncrCursors(incr Incrementer, keyPrefix string) *Int64IncrCursors {
	if keyPrefix == "" {
		keyPrefix = DefaultCursorKeyPrefix
	}
	return &Int64IncrCursors{incr: incr, prefix: keyPrefix}
}

// Next returns Incr(prefix+source) as a base-10 string.
func (p *Int64IncrCursors) Next(ctx context.Context, source string) (string, error) {
	n, err := p.incr.Incr(ctx, p.prefix+source)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(n, 10), nil
}

// CursorProvidingStore is an optional capability an EventBufferStore MAY
// implement to mint cursors itself on write, from its own global write
// sequence (issue 833). It is queried by type assertion, so existing and
// third-party EventBufferStore implementations that do not implement it
// keep working unchanged — they are simply treated as non-providing and
// the configured CursorProvider mints instead.
//
// The capability is per-source: a single store instance may back many
// sources, and only some may sit on a backend with a usable sequence
// (e.g. one SQL table has an autoincrement column, another does not). The
// runtime queries ProvidesCursor(source) once per source at registration.
//
// When ProvidesCursor(source) is true, YieldingSource calls Append with a
// nil Event.Cursor for that source; the store assigns the cursor and
// returns it in AppendEventResponse.Cursor. The store-assigned value is
// then stamped onto the event before it is buffered locally and fanned
// out. Because the cursor is not known until the write completes, the
// store-minted path fans out after Append (the we-mint providers stamp
// before Append and fan out immediately).
type CursorProvidingStore interface {
	// ProvidesCursor reports whether this store assigns cursors on write
	// for the named source. Must be cheap and side-effect-free (a
	// capability declaration from the store's own config, not a query).
	ProvidesCursor(source string) bool
}
