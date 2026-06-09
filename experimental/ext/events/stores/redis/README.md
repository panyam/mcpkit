# redisstore — Redis pubsub Emitter for `experimental/ext/events`

Cross-replica fanout for the MCP Events extension via Redis pubsub. Plugs into the [`Emitter` seam](../../emitter.go) (issue 629) — the parent package's output-side interface — so multi-replica deployments can wrap the existing single-replica behavior with peer fanout without changing application code.

Implements issue 634. See [`STORAGE_SEAMS.md`](../../STORAGE_SEAMS.md) for how this fits in the broader backend story.

## Usage

```go
import (
    "github.com/redis/go-redis/v9"
    "github.com/panyam/mcpkit/experimental/ext/events"
    redisstore "github.com/panyam/mcpkit/experimental/ext/events/stores/redis"
)

cli := redis.NewClient(&redis.Options{Addr: "redis:6379"})

opts := redisstore.Options{Client: cli}

// Outbound: compose Redis publisher with the local emitter.
local := events.NewLocalEmitter(srv, webhooks)
pub, _ := redisstore.NewPublisher(opts)
cfg.Emitter = events.NewCompositeEmitter(local, pub)

// Inbound: subscriber delivers to LOCAL only — NOT cfg.Emitter (see below).
sub, _ := redisstore.NewSubscriber(opts, func(ctx context.Context, e events.Event) error {
    return local.Emit(ctx, e)
})
sub.Subscribe(ctx, "chat.message", "presence.changed")
go sub.Run(ctx)
```

## Anti-loop pattern

A naive setup that wires the publisher into the same emitter the subscriber feeds will infinitely re-publish every event:

```
publisher → Redis → subscriber → publisher (LOOP)
```

The pattern above avoids this by handing the subscriber the **local** emitter, not the composite:

```
publisher → Redis → subscriber → local (terminal sink)
```

That's the load-bearing detail. If you're seeing every event delivered ad infinitum, this is why.

## Delivery contract

**At-most-once.** Redis pubsub drops messages on:

- Late subscribers (anyone who SUBSCRIBE's after the PUBLISH)
- Redis restart (the in-flight pipeline disappears)
- Network blip between publisher and Redis (`Emit` returns the error to the caller)
- Decode failure on the subscriber side (logged, dropped, subscriber keeps draining)

For the whole-enchilada demo (#407) this is acceptable per the data-tier acceptance criteria — counters resetting on restart is the same property.

Higher delivery floors are deferred to follow-up issues:

- **At-least-once via Redis Streams** — a separate Subscriber implementation with explicit ACK on the consumer side
- **Dedup via Redis-stored recently-delivered set** — a wrapper around the existing `DeliverFunc` that consults a `SETNX <subID:eventID>` with TTL

## Channel naming

One channel per event name, prefix-namespaced:

```
<ChannelPrefix>.<event.Name>
```

Default `ChannelPrefix` is `mcpkit.events`. Override in `Options.ChannelPrefix` if multiple isolated demo stacks share one Redis cluster.

## Codec

Wire-format is `Codec`-pluggable. Default is `JSONCodec` (`encoding/json` over the wire). Implement the `Codec` interface for protobuf, msgpack, or any other format — both publisher and subscriber MUST use the same codec.

The `Codec` interface lives in this sub-module for now. When a second cross-process backend (Kafka, NATS) wants the same shape, we promote it to the parent package.

## Testing

```
make test         # miniredis (no Docker, default)
make testredis    # real Redis via Docker
make updb         # start the Redis container long-running
make downdb       # stop it
```

The same test bodies run against either backend — `MCPKIT_EVENTS_TEST_REDIS_ADDR=<addr>` flips the test fixture from miniredis to a live Redis.

## Out of scope (this sub-module)

- Redis-backed `QuotaStore` — separate follow-up issue
- Redis Cluster topology / Sentinel HA
- Replication-aware fanout
