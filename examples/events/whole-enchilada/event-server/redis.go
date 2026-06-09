package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	redisstore "github.com/panyam/mcpkit/experimental/ext/events/stores/redis"
	"github.com/panyam/mcpkit/server"
	"github.com/redis/go-redis/v9"
)

// redisBackend bundles the Redis-backed events plumbing so main.go
// can wire it via one call site + one defer. The shutdown method
// cancels the Subscriber goroutine and closes the Redis client.
//
// When REDIS_ADDR is empty, configureRedisBackend returns a
// no-op redisBackend — main.go's defer becomes a no-op and the
// events lib uses its in-memory defaults.
type redisBackend struct {
	cli    *redis.Client
	sub    *redisstore.Subscriber
	cancel context.CancelFunc
	done   chan struct{}
}

// shutdown is safe to call on a zero redisBackend (no-op when
// REDIS_ADDR was empty).
func (r *redisBackend) shutdown() {
	if r == nil || r.cancel == nil {
		return
	}
	r.cancel()
	if r.sub != nil {
		_ = r.sub.Close()
	}
	if r.done != nil {
		<-r.done
	}
	if r.cli != nil {
		_ = r.cli.Close()
	}
}

// configureRedisBackend wires Redis-backed Emitter + QuotaStore onto
// cfg when REDIS_ADDR is set. Returns a non-nil *redisBackend so the
// caller can defer shutdown unconditionally; when REDIS_ADDR is empty
// the returned value is zero and shutdown is a no-op.
//
// Recognized env vars:
//
//	REDIS_ADDR              Required to activate. host:port form.
//	                        Empty leaves cfg untouched (in-memory).
//	REDIS_PASSWORD          Optional. Empty for unauthenticated.
//	REDIS_DB                Optional integer. Defaults to 0.
//	REDIS_CHANNEL_PREFIX    Optional. Defaults to "mcpkit.events".
//	REDIS_QUOTA_TTL         Optional Go duration. Defaults to 1h.
//
// Wiring shape (Pattern B from the redisstore README — chosen for
// the demo because it avoids double-delivery on the publishing
// replica):
//
//   - cfg.Emitter = redisstore.Publisher.  Outbound is Redis-only.
//     Events yielded on replica A reach A's own SSE + webhook
//     listeners via the round-trip-through-Redis path, NOT via a
//     local fan-out on emit.
//   - Subscriber.deliverFn = events.NewLocalEmitter(srv, webhooks).
//     Every replica including the publisher receives the same
//     PUBLISHed event and delivers it locally exactly once.
//
// The redisstore README's "compose local + redis publisher" pattern
// (Pattern A) is simpler but causes double-delivery on the publishing
// replica because that replica is also a subscriber to its own
// channel; that pattern is fine when the application can filter
// self-published messages but the demo doesn't have that plumbing.
func configureRedisBackend(cfg *events.Config, srv *server.Server, webhooks *events.WebhookRegistry) *redisBackend {
	addr := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if addr == "" {
		log.Printf("[event-server] Redis backend: disabled (REDIS_ADDR empty); using in-memory defaults")
		return &redisBackend{}
	}

	db := 0
	if raw := strings.TrimSpace(os.Getenv("REDIS_DB")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			db = n
		}
	}
	cli := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       db,
	})

	channelPrefix := strings.TrimSpace(os.Getenv("REDIS_CHANNEL_PREFIX"))
	quotaTTL := time.Duration(0) // 0 → withDefaults applies 1h
	if raw := strings.TrimSpace(os.Getenv("REDIS_QUOTA_TTL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			quotaTTL = d
		}
	}
	opts := redisstore.Options{
		Client:        cli,
		ChannelPrefix: channelPrefix, // empty → withDefaults applies "mcpkit.events"
		QuotaTTL:      quotaTTL,
	}

	// Quota — Redis-backed atomic counters. Caps stay the same as
	// the in-memory default would have been (none — the demo doesn't
	// configure per-event-type caps today). When caps are added, they
	// go through WithMaxSubscriptionsPerPrincipal alongside this call.
	qs, err := redisstore.NewQuotaStore(opts)
	if err != nil {
		log.Fatalf("redisstore.NewQuotaStore: %v", err)
	}
	cfg.Quota = events.NewQuota(events.WithQuotaStore(qs))

	// Emitter — Redis-only outbound (Pattern B; see the doc comment
	// above). The receive side below delivers locally.
	pub, err := redisstore.NewPublisher(opts)
	if err != nil {
		log.Fatalf("redisstore.NewPublisher: %v", err)
	}
	cfg.Emitter = pub

	// Subscriber — receives PUBLISHed events from Redis and delivers
	// each to the LOCAL emitter, which broadcasts to SSE listeners and
	// POSTs to webhook targets on this replica. On a single-replica
	// stack this round-trips locally through Redis (small latency tax;
	// keeps the wiring symmetric across N=1 and N>=2). On a
	// multi-replica stack every replica including the publisher
	// participates equally.
	local := events.NewLocalEmitter(srv, webhooks)
	sub, err := redisstore.NewSubscriber(opts, local.Emit)
	if err != nil {
		log.Fatalf("redisstore.NewSubscriber: %v", err)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	// Subscribe to the channels this demo's event sources actually
	// emit. Future event types added to chatSrc / presenceSrc must
	// be appended here too (or this whole list moved to a single
	// source-of-truth registry).
	if err := sub.Subscribe(subCtx, "chat.message", "presence.changed"); err != nil {
		cancel()
		log.Fatalf("redisstore Subscribe: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := sub.Run(subCtx); err != nil {
			log.Printf("[event-server] redis subscriber exited: %v", err)
		}
	}()

	// Surface the resolved (post-default) values so an operator
	// inspecting logs sees the same prefix/TTL the store actually
	// applied, not the bare struct fields.
	effPrefix := opts.ChannelPrefix
	if effPrefix == "" {
		effPrefix = redisstore.DefaultChannelPrefix
	}
	effTTL := opts.QuotaTTL
	if effTTL == 0 {
		effTTL = redisstore.DefaultQuotaTTL
	}
	log.Printf("[event-server] Redis backend active: addr=%s prefix=%s quotaTTL=%s",
		addr, effPrefix, effTTL)

	return &redisBackend{cli: cli, sub: sub, cancel: cancel, done: done}
}
