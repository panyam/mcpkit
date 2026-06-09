package redisstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// reserveScript is the Lua atomic primitive backing ReserveQuota.
// One round trip — Redis runs the body to completion before any
// other command on the same key, so the check + increment can't
// race against concurrent Reserves under any client load.
//
// KEYS[1] is the counter key. ARGV[1] is the cap (Max from the
// request). ARGV[2] is the sliding TTL window in seconds.
//
// Returns a two-element array:
//
//	{1, count}   — granted, count is the post-increment value
//	{0, count}   — rejected (count >= Max), count is the value
//	               that blocked the reservation
//
// EXPIRE is applied on every Reserve so the TTL slides forward with
// activity. A leaked Reserve (caller crashed before Release) drops
// after Options.QuotaTTL of inactivity; active counters stay alive
// under load.
var reserveScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
local max = tonumber(ARGV[1])
if current >= max then
  return {0, current}
end
local next = redis.call("INCR", KEYS[1])
redis.call("EXPIRE", KEYS[1], ARGV[2])
return {1, next}
`)

// releaseScript decrements the counter and deletes the key when the
// post-decrement value is <= 0. Two ops in one server-side execution
// — keeps Redis's KEYS view clean (no zero-value rows lying around)
// and removes the timing window between DECR returning 0 and a
// concurrent Reserve observing it.
//
// Release at zero is a benign no-op (matches the in-memory store's
// contract: double-release shouldn't underflow). If GET returns nil
// (key already expired or never existed), the script returns 0
// without touching anything.
var releaseScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
if current <= 0 then
  return 0
end
local next = redis.call("DECR", KEYS[1])
if next <= 0 then
  redis.call("DEL", KEYS[1])
end
return next
`)

// QuotaStore implements events.QuotaStore over Redis atomic counters.
// One key per (Principal, EventName) tuple under
// "<Options.ChannelPrefix>.quota.<principal>.<eventName>".
//
// ReserveQuota and ReleaseQuota are both atomic via per-call Lua
// scripts cached at process startup (EVALSHA, with EVAL fallback on
// NOSCRIPT). CountQuota is a plain GET — no atomicity needed for
// inspection.
type QuotaStore struct {
	opts Options
}

// NewQuotaStore returns a Redis-backed events.QuotaStore. Returns an
// error when Options.Client is nil or Options.QuotaTTL is negative.
// Zero QuotaTTL means "use DefaultQuotaTTL" (1 hour).
func NewQuotaStore(opts Options) (*QuotaStore, error) {
	if opts.Client == nil {
		return nil, errors.New("redisstore.NewQuotaStore: Options.Client is required")
	}
	if opts.QuotaTTL < 0 {
		return nil, fmt.Errorf("redisstore.NewQuotaStore: Options.QuotaTTL must be >= 0; got %s", opts.QuotaTTL)
	}
	return &QuotaStore{opts: opts.withDefaults()}, nil
}

// ReserveQuota claims one slot for (Principal, EventName) only if
// the current count is strictly less than Max. Atomic via a Lua
// script — concurrent Reserves on the same key never race. Refreshes
// the key's TTL on success so active counters slide forward with
// activity.
//
// Returns Granted=true + the post-increment Count on success;
// Granted=false + the blocking Count on cap. Storage errors (Redis
// connection drop, NOSCRIPT mid-script-cache transition) surface as
// the second return.
func (s *QuotaStore) ReserveQuota(ctx context.Context, req events.ReserveQuotaRequest) (events.ReserveQuotaResponse, error) {
	key := s.opts.quotaKeyFor(req.Principal, req.EventName)
	ttlSecs := int(s.opts.QuotaTTL.Seconds())
	raw, err := reserveScript.Run(ctx, s.opts.Client, []string{key}, req.Max, ttlSecs).Result()
	if err != nil {
		return events.ReserveQuotaResponse{}, fmt.Errorf("redisstore: ReserveQuota EVAL failed: %w", err)
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) != 2 {
		return events.ReserveQuotaResponse{}, fmt.Errorf("redisstore: ReserveQuota: unexpected script return shape %T %v", raw, raw)
	}
	granted, _ := arr[0].(int64)
	count, _ := arr[1].(int64)
	return events.ReserveQuotaResponse{
		Granted: granted == 1,
		Count:   int(count),
	}, nil
}

// ReleaseQuota returns one slot for (Principal, EventName). Decrements
// the counter if currently > 0; deletes the key when the decrement
// brings it to zero (keeps Redis's KEYS view clean). Release-at-zero
// is a silent no-op — matches the in-memory store's contract so
// double-release doesn't underflow.
func (s *QuotaStore) ReleaseQuota(ctx context.Context, req events.ReleaseQuotaRequest) (events.ReleaseQuotaResponse, error) {
	key := s.opts.quotaKeyFor(req.Principal, req.EventName)
	if _, err := releaseScript.Run(ctx, s.opts.Client, []string{key}).Result(); err != nil {
		return events.ReleaseQuotaResponse{}, fmt.Errorf("redisstore: ReleaseQuota EVAL failed: %w", err)
	}
	return events.ReleaseQuotaResponse{}, nil
}

// CountQuota reads the current count for (Principal, EventName)
// without mutating state. Returns 0 for absent keys (never reserved,
// or already TTL-expired). Storage errors surface as the second
// return; Redis's "key does not exist" is NOT an error — it's
// just Count=0.
func (s *QuotaStore) CountQuota(ctx context.Context, req events.CountQuotaRequest) (events.CountQuotaResponse, error) {
	key := s.opts.quotaKeyFor(req.Principal, req.EventName)
	v, err := s.opts.Client.Get(ctx, key).Int()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return events.CountQuotaResponse{Count: 0}, nil
		}
		return events.CountQuotaResponse{}, fmt.Errorf("redisstore: CountQuota GET failed: %w", err)
	}
	return events.CountQuotaResponse{Count: v}, nil
}

// Compile-time check that *QuotaStore satisfies events.QuotaStore.
var _ events.QuotaStore = (*QuotaStore)(nil)
