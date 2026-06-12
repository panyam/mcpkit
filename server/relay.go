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
// Server.Broadcast with a background context — the relay is
// fire-and-forget at the notification level; the transport's ctx
// cancellation belongs to the transport loop, not to the broadcast
// fan-out on this replica.
func (r *CapabilityBroadcastReceiver) ReceiveRelay(_ context.Context, method string, params any) {
	r.srv.Broadcast(context.Background(), method, params)
}
