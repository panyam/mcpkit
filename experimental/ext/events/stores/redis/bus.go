// bus.go — bundled Pattern B Publisher + Subscriber.
//
// Bus is the recommended adopter entry point for redisstore. It owns a
// per-instance origin marker, wires its Publisher and Subscriber
// against that marker (so self-publishes are dropped before they
// re-fire local handlers), and exposes a small surface:
//
//   bus, err := redisstore.NewBus(opts, receiver)   // construct
//   bus.Emit(ctx, event)                            // implements events.Emitter
//   bus.Subscribe(ctx, "chat.message", ...)         // declare channels
//   bus.Run(ctx)                                    // start receive loop
//   bus.Close()                                     // tear down
//
// receiver is a server.NotificationRelayReceiver — the routing
// abstraction mcpkit exposes for cross-replica notification delivery.
// For events specifically, pass an events.LocalDeliverer-shaped
// receiver that knows how to look up the source by event.Name and call
// LocalDeliver. For capability-shaped notifications (tools/list_changed
// etc.), pass server.NewCapabilityBroadcastReceiver.
//
// The Publisher and Subscriber types remain exported for adopters who
// need lower-level control, but the standard adoption path is Bus.

package redisstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

// EventMethodName is the JSON-RPC method name redisstore.Bus uses when
// invoking server.NotificationRelayReceiver.ReceiveRelay for events
// delivered via this transport. Exposed so adopters writing custom
// receivers can switch on it without hard-coding the string.
const EventMethodName = "notifications/events/event"

// Bus bundles a Publisher (outbound) + Subscriber (inbound) against a
// shared per-instance origin marker. Implements events.Emitter so it
// can be wired into events.Config.Emitter directly.
//
// Concurrency: Emit is safe for concurrent calls. Subscribe / Run /
// Close should be called from a single goroutine after construction
// (typical pattern: Subscribe once, Run in a goroutine, Close on
// shutdown).
type Bus struct {
	pub *Publisher
	sub *Subscriber
}

// NewBus constructs a Bus wiring a Publisher + Subscriber against a
// shared origin marker. The receiver is invoked once per cross-replica
// event received (after self-publish filtering). Returns an error when
// opts.Client is nil or the underlying Publisher / Subscriber
// constructors fail.
//
// The receiver's ReceiveRelay is called with method = EventMethodName
// and params = the decoded events.Event. Receivers implementing
// domain-specific routing (events.YieldingSource via a thin adapter,
// future ResourcesUpdatedRouter, etc.) type-assert params to their
// expected shape.
func NewBus(opts Options, receiver server.NotificationRelayReceiver) (*Bus, error) {
	if receiver == nil {
		return nil, errors.New("redisstore.NewBus: receiver is required")
	}
	pub, err := NewPublisher(opts)
	if err != nil {
		return nil, fmt.Errorf("redisstore.NewBus: publisher: %w", err)
	}
	// Bus wires its own subscriber against the publisher's origin
	// marker so self-publishes are dropped at the transport layer
	// before the receiver sees them.
	opts.skipOriginID = pub.originID
	sub, err := NewSubscriber(opts, func(ctx context.Context, event events.Event) error {
		receiver.ReceiveRelay(ctx, EventMethodName, event)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("redisstore.NewBus: subscriber: %w", err)
	}
	return &Bus{pub: pub, sub: sub}, nil
}

// Emit implements events.Emitter — publishes the event via the
// internal Publisher. Origin marker handling is automatic.
func (b *Bus) Emit(ctx context.Context, event events.Event) error {
	return b.pub.Emit(ctx, event)
}

// Subscribe declares the channels the internal Subscriber listens on.
// Calling Subscribe with the same name twice is a no-op for that name.
// Add new channels at any time before or after Run.
func (b *Bus) Subscribe(ctx context.Context, eventNames ...string) error {
	return b.sub.Subscribe(ctx, eventNames...)
}

// Run starts the receive loop. Blocks until ctx is cancelled or the
// underlying pubsub channel closes. Typical use:
//
//	go bus.Run(ctx)
//
// Returns nil on clean shutdown; the underlying pubsub error
// otherwise.
func (b *Bus) Run(ctx context.Context) error {
	return b.sub.Run(ctx)
}

// Close tears down the Subscriber's pubsub connection. Idempotent.
// Does NOT close the underlying *redis.Client — adopter owns the
// client's lifecycle.
func (b *Bus) Close() error {
	return b.sub.Close()
}

// Compile-time check: Bus satisfies events.Emitter.
var _ events.Emitter = (*Bus)(nil)
