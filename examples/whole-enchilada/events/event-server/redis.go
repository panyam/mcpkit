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

// parseQuotaCaps decodes the EVENTS_QUOTA_CAPS env shape into a
// per-event cap map. Format:
//
//	EVENTS_QUOTA_CAPS=chat.message=3,presence.changed=10
//
// Whitespace around tokens is tolerated. Entries with missing /
// non-positive integer values are silently dropped — bad config
// should never crash the event-server at boot. The empty string
// produces an empty map (no caps applied; every Reserve succeeds).
func parseQuotaCaps(raw string) map[string]int {
	out := map[string]int{}
	for _, kv := range strings.Split(raw, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(kv[:eq])
		nRaw := strings.TrimSpace(kv[eq+1:])
		n, err := strconv.Atoi(nRaw)
		if err != nil || n <= 0 || name == "" {
			continue
		}
		out[name] = n
	}
	return out
}

// redisBackend bundles the Redis-backed events plumbing so main.go
// can wire it via one call site + one defer. The shutdown method
// cancels the Subscriber goroutine and closes the Redis client.
//
// When REDIS_ADDR is empty, configureRedisBackend returns a
// no-op redisBackend — main.go's defer becomes a no-op and the
// events lib uses its in-memory defaults.
type redisBackend struct {
	cli    *redis.Client
	bus    *redisstore.Bus
	cancel context.CancelFunc
	done   chan struct{}
	// registry is populated by the caller AFTER events.Register returns
	// — the router closure dereferences this to look up the source for
	// ReceiveRelay routing. nil until SetRegistry runs; receives that
	// arrive before then are dropped (acceptable: yields can't happen
	// yet because the source handlers aren't installed).
	registry *events.Registry
}

// SetRegistry hands the post-Register events.Registry to the router so
// it can look up sources on cross-replica receives. Idempotent — only
// the first call has effect.
func (r *redisBackend) SetRegistry(reg *events.Registry) {
	if r == nil || r.registry != nil {
		return
	}
	r.registry = reg
}

// shutdown is safe to call on a zero redisBackend (no-op when
// REDIS_ADDR was empty).
func (r *redisBackend) shutdown() {
	if r == nil || r.cancel == nil {
		return
	}
	r.cancel()
	if r.bus != nil {
		_ = r.bus.Close()
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

	// Quota — Redis-backed atomic counters with per-event-type caps
	// parsed out of EVENTS_QUOTA_CAPS (see parseQuotaCaps). The default
	// in compose.tmpl is "chat.message=3" — small enough that the
	// walkthrough's "subscribe 4 times, watch the 4th get -32013" beat
	// is easy to drive by hand. Empty / unset env => no caps (every
	// Reserve succeeds; matches the pre-cap demo behavior).
	qs, err := redisstore.NewQuotaStore(opts)
	if err != nil {
		log.Fatalf("redisstore.NewQuotaStore: %v", err)
	}
	quotaOpts := []events.QuotaOption{events.WithQuotaStore(qs)}
	for eventName, max := range parseQuotaCaps(os.Getenv("EVENTS_QUOTA_CAPS")) {
		quotaOpts = append(quotaOpts, events.WithMaxSubscriptionsPerPrincipal(eventName, max))
		log.Printf("[event-server] quota cap: %s = %d subscriptions/principal", eventName, max)
	}
	cfg.Quota = events.NewQuota(quotaOpts...)

	// rb pre-allocated so the router closure (below) can read
	// rb.registry, which the caller sets via SetRegistry AFTER
	// events.Register returns (the registry doesn't exist yet at this
	// point).
	rb := &redisBackend{cli: cli}

	// Pattern B Bus — bundles outbound Publisher + inbound Subscriber
	// with self-publish dedup hidden inside (origin marker handling is
	// transport-internal; see stores/redis/origin.go). The router
	// adapter dispatches each received event to the matching
	// YieldingSource on this replica so per-slot Match / Transform
	// runs the same as for a same-replica yield — tenant scoping etc.
	// stay authoritative.
	router := &registryRouter{rb: rb}
	bus, err := redisstore.NewBus(opts, router)
	if err != nil {
		log.Fatalf("redisstore.NewBus: %v", err)
	}

	// Yield-side emitter: deliver locally to webhooks (origin replica
	// fires webhook POSTs exactly once globally per yielded event) AND
	// publish via Bus for cross-replica relay. Stream subscribers on
	// this replica receive via the source's subscriber-slot channel —
	// NOT via Server.Broadcast — so we don't need a Broadcast call on
	// the yield path here.
	cfg.Emitter = events.NewCompositeEmitter(
		events.NewLocalEmitter(srv, webhooks),
		bus,
	)

	subCtx, cancel := context.WithCancel(context.Background())
	// Subscribe to the channels this demo's event sources actually
	// emit. Future event types added to chatSrc / presenceSrc must
	// be appended here too (or this whole list moved to a single
	// source-of-truth registry).
	if err := bus.Subscribe(subCtx, "chat.message", "presence.changed"); err != nil {
		cancel()
		log.Fatalf("redisstore Bus Subscribe: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := bus.Run(subCtx); err != nil {
			log.Printf("[event-server] redis bus exited: %v", err)
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

	rb.bus = bus
	rb.cancel = cancel
	rb.done = done
	return rb
}

// registryRouter implements server.NotificationRelayReceiver by looking
// up the destination YieldingSource on the registry and forwarding to
// its own ReceiveRelay. The lookup runs on every cross-replica event
// because rb.registry only becomes available AFTER events.Register
// returns (the registry doesn't exist at configureRedisBackend time).
// Events that arrive before SetRegistry has been called are silently
// dropped — vanishingly rare and harmless (no source can yield yet).
type registryRouter struct {
	rb *redisBackend
}

func (r *registryRouter) ReceiveRelay(ctx context.Context, method string, params any) {
	if r.rb.registry == nil {
		return
	}
	ev, ok := params.(events.Event)
	if !ok {
		return
	}
	src, ok := r.rb.registry.Source(ev.Name)
	if !ok {
		return
	}
	nrr, ok := src.(server.NotificationRelayReceiver)
	if !ok {
		// Sources that don't implement NotificationRelayReceiver
		// (TypedSource today) silently skip cross-replica push
		// delivery. Their webhook subscribers still receive events via
		// the origin replica's EmitToWebhooks; only push misses out.
		return
	}
	nrr.ReceiveRelay(ctx, method, params)
}
