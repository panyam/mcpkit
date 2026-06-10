package gormstore

import "time"

// Option configures a GORM-backed store at construction.
type Option func(*config)

type config struct {
	skipAutoMigrate bool
	bufferTTL       time.Duration
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
