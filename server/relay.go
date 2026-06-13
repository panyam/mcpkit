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
	"sync"

	"github.com/panyam/mcpkit/core"
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

// ResourcesUpdatedReceiver is the reference NotificationRelayReceiver
// for the subscription-shaped notifications/resources/updated. On
// ReceiveRelay it extracts the URI from the payload and dispatches via
// Server.NotifyResourceUpdatedLocal — fires every locally-subscribed
// session whose subscribe(URI) matches, WITHOUT re-publishing via the
// BroadcastRelay (which would loop).
//
// Each replica's subscriptionRegistry independently filters by its
// own per-URI subscriber set; clients subscribed via THIS replica's
// session hear the cross-replica notification, clients on other
// replicas hear it via THEIR own ResourcesUpdatedReceiver instance.
type ResourcesUpdatedReceiver struct {
	srv *Server
}

// NewResourcesUpdatedReceiver constructs a ResourcesUpdatedReceiver
// wired to srv. The Server's existing subscriptionRegistry handles
// per-URI subscriber lookup; this receiver just adapts the cross-
// replica notification shape into that local call.
func NewResourcesUpdatedReceiver(srv *Server) *ResourcesUpdatedReceiver {
	return &ResourcesUpdatedReceiver{srv: srv}
}

// ReceiveRelay implements NotificationRelayReceiver. Expects params to
// be a core.ResourceUpdatedNotification (the type
// subscriptionRegistry.notify publishes) — also accepts
// map[string]any with a "uri" field for transports that decoded
// generically. Unknown shapes are silently dropped.
func (r *ResourcesUpdatedReceiver) ReceiveRelay(_ context.Context, method string, params any) {
	if method != "notifications/resources/updated" {
		return
	}
	uri := uriFromUpdatedParams(params)
	if uri == "" {
		return
	}
	r.srv.NotifyResourceUpdatedLocal(uri)
}

// uriFromUpdatedParams pulls the URI out of whichever shape the
// transport handed us. core.ResourceUpdatedNotification is the
// authoritative type produced by the publish side; receivers that
// decode the wire payload generically end up with map[string]any.
// We accept both shapes so adopters writing custom transports don't
// have to know the typed shape.
func uriFromUpdatedParams(params any) string {
	switch v := params.(type) {
	case core.ResourceUpdatedNotification:
		return v.URI
	case map[string]any:
		s, _ := v["uri"].(string)
		return s
	}
	return ""
}

// MultiplexRelayReceiver routes received notifications by method name
// to per-method NotificationRelayReceivers. Adopters configure one
// MultiplexRelayReceiver as the BroadcastRelay's receiver, registering
// per-method handlers (CapabilityBroadcastReceiver for catalog
// notifications, ResourcesUpdatedReceiver for subscription-shaped
// resources/updated, etc.). Methods without a registered handler are
// dropped silently — appropriate for shared Pattern B transports where
// not every replica subscribes to every method.
//
// Concurrency: Handle and ReceiveRelay are safe for concurrent use.
// Routing decisions snapshot the dispatch table under a read lock so
// in-flight notifications don't race with late registrations.
type MultiplexRelayReceiver struct {
	mu       sync.RWMutex
	handlers map[string]NotificationRelayReceiver
}

// NewMultiplexRelayReceiver constructs an empty multiplexer. Register
// per-method handlers via Handle before installing the multiplexer on
// a BroadcastRelay's receiver slot.
func NewMultiplexRelayReceiver() *MultiplexRelayReceiver {
	return &MultiplexRelayReceiver{handlers: map[string]NotificationRelayReceiver{}}
}

// Handle registers receiver as the handler for the given method.
// Returns the multiplexer for chaining. A subsequent Handle call for
// the same method replaces the previous handler.
func (m *MultiplexRelayReceiver) Handle(method string, receiver NotificationRelayReceiver) *MultiplexRelayReceiver {
	if receiver == nil {
		return m
	}
	m.mu.Lock()
	m.handlers[method] = receiver
	m.mu.Unlock()
	return m
}

// ReceiveRelay implements NotificationRelayReceiver. Looks up the
// per-method handler and forwards. Unknown methods are silently
// dropped.
func (m *MultiplexRelayReceiver) ReceiveRelay(ctx context.Context, method string, params any) {
	m.mu.RLock()
	h := m.handlers[method]
	m.mu.RUnlock()
	if h == nil {
		return
	}
	h.ReceiveRelay(ctx, method, params)
}
