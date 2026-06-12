// relay.go — cross-replica notification routing primitives.
//
// In a multi-replica deployment, mcpkit servers need a way for
// notifications generated on replica K to reach the connected clients
// on replicas K′. Pattern B is the wiring shape mcpkit recommends:
// each replica runs a publisher + subscriber pair against a shared
// pub/sub transport (Redis pubsub, Kafka, NATS, etc.); the publisher
// pushes notifications outward; the subscriber on every replica
// receives them and routes into the local per-replica delivery
// machinery (per-source slots, per-URI subscriptions,
// session-attached SSE listeners).
//
// Two routing categories cover every notification surface mcpkit
// emits today:
//
//   - **Capability-shaped** (notifications/tools/list_changed,
//     notifications/resources/list_changed,
//     notifications/prompts/list_changed): no per-subscription filter
//     — every connected client that declared the capability sees the
//     notification. Use CapabilityBroadcastReceiver.
//
//   - **Subscription-shaped** (notifications/events/event,
//     notifications/resources/updated): each replica applies a
//     per-subscription filter (events EventDef.Match per slot, URI
//     prefix per resources/subscribe). Routing happens through
//     domain-specific adapters (events.YieldingSource for events;
//     equivalent for resources/updated when that wire-up lands).
//
// NotificationRelayReceiver is the shared interface a Pattern B
// transport adapter calls on receive. Self-publish dedup (avoiding a
// replica re-firing its own publishes) is the transport adapter's
// internal concern — receivers don't see it.
//
// Source: issue 755.
package server

import (
	"context"
)

// BroadcastRelay is the publish side of a Pattern B transport for
// capability-shaped notifications (notifications/tools/list_changed,
// notifications/resources/list_changed, etc.). Adopters install one
// via WithBroadcastRelay; Server.Broadcast then fires
// PublishBroadcast(ctx, method, params) before the local
// BroadcastToSessions fan-out so the same notification reaches every
// connected client across every replica.
//
// PublishBroadcast is fire-and-forget — Server.Broadcast does not
// surface errors. Implementations log internally if a publish fails;
// the local BroadcastToSessions fan-out still runs regardless.
//
// Concurrency: PublishBroadcast may be invoked from any goroutine that
// calls Server.Broadcast (handlers, registry.OnChange, etc.).
// Implementations must be safe for concurrent calls.
//
// The receive-side wiring (a separate subscriber loop calling
// Server.BroadcastToSessions on cross-replica receives) is the
// transport adapter's responsibility. The reference Redis
// implementation is redisstore.CapabilityBus, which satisfies
// BroadcastRelay on the publish side AND drives BroadcastToSessions
// on the subscribe side via a NotificationRelayReceiver wired
// internally.
type BroadcastRelay interface {
	PublishBroadcast(ctx context.Context, method string, params any)
}

// NotificationRelayReceiver is the callback shape mcpkit expects from
// a Pattern B transport's subscriber side. The transport adapter calls
// ReceiveRelay once per cross-replica notification destined for this
// replica (i.e. after the transport's own self-publish filtering).
//
// Implementations are domain-specific:
//   - CapabilityBroadcastReceiver forwards to Server.Broadcast for
//     catalog-mutation notifications.
//   - events.YieldingSource routes via per-slot fanout so per-
//     subscription Match / Transform run.
//   - A future ResourcesUpdatedRouter (PR B) routes via the per-URI
//     subscription set with prefix match.
//
// Concurrency: ReceiveRelay may be invoked from any goroutine
// (typically the transport's receive loop). Implementations must be
// safe for concurrent calls.
type NotificationRelayReceiver interface {
	ReceiveRelay(ctx context.Context, method string, params any)
}

// WithBroadcastRelay installs a BroadcastRelay onto the Server.
// Server.Broadcast then fires PublishBroadcast on the relay before
// running BroadcastToSessions locally — every capability-shaped
// notification (tools/list_changed, resources/list_changed,
// prompts/list_changed, application-level broadcasts) reaches
// connected clients across every replica in the deployment.
//
// Pair with a transport adapter's subscribe-side wiring that calls
// Server.BroadcastToSessions on cross-replica receives (the reference
// is redisstore.CapabilityBus, which provides both ends).
//
// Pass nil to disable the relay (default) — Server.Broadcast then
// fires local-only, matching pre-Pattern-B behavior.
func WithBroadcastRelay(relay BroadcastRelay) Option {
	return func(o *serverOptions) {
		o.broadcastRelay = relay
	}
}

// CapabilityBroadcastReceiver is the reference NotificationRelayReceiver
// for capability-shaped notifications — tools/list_changed,
// resources/list_changed, prompts/list_changed, and any future
// notification with the same shape (no per-subscription filter; every
// connected client with the capability sees it).
//
// On ReceiveRelay it forwards to Server.Broadcast, which the
// per-transport session machinery fans out to every connected client
// on this replica.
type CapabilityBroadcastReceiver struct {
	srv *Server
}

// NewCapabilityBroadcastReceiver constructs a CapabilityBroadcastReceiver
// wired to srv. The transport adapter handles self-publish dedup
// internally before invoking ReceiveRelay — adopters don't pass an
// origin marker here.
func NewCapabilityBroadcastReceiver(srv *Server) *CapabilityBroadcastReceiver {
	return &CapabilityBroadcastReceiver{srv: srv}
}

// ReceiveRelay implements NotificationRelayReceiver. Forwards to
// Server.BroadcastToSessions (NOT Server.Broadcast) with a background
// context — calling Broadcast here would re-fire the installed
// BroadcastRelay and loop indefinitely through the Pattern B
// transport. BroadcastToSessions delivers to local sessions only,
// which is what the relay receive path needs.
//
// The relay is fire-and-forget at the notification level; the
// transport's ctx cancellation belongs to the transport loop, not to
// the broadcast fan-out on this replica.
func (r *CapabilityBroadcastReceiver) ReceiveRelay(_ context.Context, method string, params any) {
	r.srv.BroadcastToSessions(context.Background(), method, params)
}
