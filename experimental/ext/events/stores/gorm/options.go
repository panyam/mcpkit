package gormstore

import "time"

// Option configures a GORM-backed store at construction.
type Option func(*config)

type config struct {
	skipAutoMigrate bool
	bufferTTL       time.Duration
	provideCursors  bool
}

// defaultBufferTTL is the per-event retention window for
// EventBufferStore rows when WithBufferTTL is not set. 1 hour matches
// the documented default for the Redis-backed QuotaStore (issue 718)
// — a leak-recovery floor that's long enough for any plausible
// Reserve → Release loop yet short enough that the database doesn't
// grow unbounded under steady-state load.
const defaultBufferTTL = time.Hour

func defaultConfig() *config { return &config{bufferTTL: defaultBufferTTL} }

// WithoutAutoMigrate disables the AutoMigrate call at store
// construction. Use this in production where schema changes are
// managed by an out-of-band migration tool — AutoMigrate is the
// dev / demo default but is not the long-term schema-evolution story.
func WithoutAutoMigrate() Option {
	return func(c *config) { c.skipAutoMigrate = true }
}

// WithBufferTTL overrides the EventBufferStore's per-event retention
// window. Every Append stamps expires_at = NOW() + ttl; the background
// eviction sweeper drops rows past their expiry. Pass <=0 to keep the
// default (defaultBufferTTL = 1h).
//
// Has no effect on the GORM-backed WebhookStore or QuotaStore — they
// handle their own expiry semantics (webhook subscription TTL is on
// each target's ExpiresAt field; quota counters are unbounded by
// design).
func WithBufferTTL(ttl time.Duration) Option {
	return func(c *config) {
		if ttl > 0 {
			c.bufferTTL = ttl
		}
	}
}

// WithProvideCursors makes the store assign cursors on write from its
// database sequence (the sequence_no autoincrement), advertising the
// CursorProvidingStore capability (issue 833). A YieldingSource wired to
// this store then leaves Event.Cursor nil and the store stamps a
// globally-monotone cursor on Append — so N replicas writing the same
// source get gap-free, collision-free cursors without a shared in-process
// counter or Redis.
//
// Off by default: without it, the store persists the caller's cursor
// verbatim (the historical behavior), and cursor minting stays with the
// YieldingSource's CursorProvider. Turning it on is a cursor-provenance
// change — existing cursor values are per-replica, new ones are the DB
// sequence — so enable it on a fresh deployment or accept that in-flight
// clients re-subscribe (which the Truncated signal already handles).
//
// The store mints for every source it backs (one table, one sequence),
// so the per-source ProvidesCursor argument is ignored here.
func WithProvideCursors() Option {
	return func(c *config) { c.provideCursors = true }
}
