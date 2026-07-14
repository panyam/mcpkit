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
//   - RedisCursors — a shared INCR counter (cross-replica, restart-safe).
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
// same source mint colliding cursors. Use RedisCursors or a
// cursor-providing store for multi-writer sources (issue 833).
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

// RedisIncr is the minimal contract RedisCursors needs from a Redis
// client: an atomic INCR that returns the new value. go-redis's
// (*redis.Client).Incr satisfies it through a one-line adapter, so core
// events takes no dependency on any Redis client.
type RedisIncr interface {
	Incr(ctx context.Context, key string) (int64, error)
}

// RedisCursors is a CursorProvider backed by a shared Redis INCR counter,
// one key per source. It is monotone and unique across every replica
// sharing the Redis instance, and survives process restarts, so it fixes
// the multi-writer / restart collision InProcessCursors has (issue 833)
// without requiring a cursor-providing store.
type RedisCursors struct {
	incr   RedisIncr
	prefix string
}

// DefaultRedisCursorPrefix is the key prefix RedisCursors uses when none
// is supplied. The per-source key is prefix + source.
const DefaultRedisCursorPrefix = "events:cursor:"

// NewRedisCursors returns a Redis-backed CursorProvider. keyPrefix is
// prepended to the source name to form the counter key; empty uses
// DefaultRedisCursorPrefix.
func NewRedisCursors(incr RedisIncr, keyPrefix string) *RedisCursors {
	if keyPrefix == "" {
		keyPrefix = DefaultRedisCursorPrefix
	}
	return &RedisCursors{incr: incr, prefix: keyPrefix}
}

// Next returns INCR(prefix+source) as a base-10 string.
func (p *RedisCursors) Next(ctx context.Context, source string) (string, error) {
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
