// Package redisstore implements panyam/mcpkit's experimental/ext/events
// Emitter seam using Redis pubsub. Cross-replica fanout for the
// whole-enchilada (#407) demo and other multi-replica deployments.
//
// # Anti-loop composition pattern
//
// A naive setup that wires the Redis publisher into the SAME emitter
// the Redis subscriber feeds will infinitely re-publish every event.
// The right shape is two distinct sinks:
//
//	local := events.NewLocalEmitter(srv, webhooks)
//	pub   := redisstore.NewPublisher(opts)
//	cfg.Emitter = events.NewCompositeEmitter(local, pub) // outbound side
//
//	sub := redisstore.NewSubscriber(opts, local.Emit) // delivers to LOCAL only
//	go sub.Run(ctx)
//
// Notice the subscriber's deliverFn is `local.Emit`, NOT `cfg.Emitter`.
// That's the load-bearing detail.
//
// # Delivery contract: at-most-once
//
// Redis pubsub is at-most-once on its own. Late subscribers miss
// messages published before they SUBSCRIBE'd; a Redis restart drops
// the in-flight pipeline; a network blip drops the message. For the
// whole-enchilada demo this is acceptable per #407's data-tier
// acceptance criteria — counters resetting on restart is the
// equivalent property and is explicitly tolerated.
//
// Higher delivery floors (at-least-once via Redis Streams; dedup via
// a Redis-stored recently-delivered set with TTL) are deferred to
// follow-up issues. v1 is intentionally minimal.
//
// # Channel naming
//
// One channel per event name, prefix-namespaced:
//
//	mcpkit.events.<event.Name>
//
// Subscribers SUBSCRIBE to the specific channels they care about; the
// filtering happens on the Redis side, so a replica that only consumes
// chat.message doesn't pay for unrelated traffic.
package redisstore
