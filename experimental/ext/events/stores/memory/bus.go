// memorystore — in-memory Pattern B transport for the events SDK.
//
// memorystore.Bus implements events.Emitter + the Subscribe / Run /
// Close lifecycle exactly like redisstore.Bus, but uses a shared
// in-process Hub instead of Redis pubsub. Used for multi-replica
// tests of mcpkit's events surface without standing up Redis.
//
// Typical test wiring:
//
//	hub := memorystore.NewHub()
//	busA, _ := memorystore.NewBus(hub, receiverA)
//	busB, _ := memorystore.NewBus(hub, receiverB)
//	busA.Subscribe(ctx, "chat.message")
//	busB.Subscribe(ctx, "chat.message")
//	go busA.Run(ctx)
//	go busB.Run(ctx)
//	busA.Emit(ctx, events.Event{Name: "chat.message", ...})
//	// busB's receiver fires; busA's does not (self-publish dedup)
//
// Production code does not depend on this package — the canonical
// transport is redisstore. This package exists so multi-replica
// behavior can be exercised in fast in-process tests covering tenant
// scoping, per-slot Match cross-replica, and N×T×M fanout.

package memorystore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

// EventMethodName mirrors redisstore.EventMethodName — the JSON-RPC
// method the in-memory Bus uses when invoking the receiver on every
// cross-replica event delivery.
const EventMethodName = "notifications/events/event"

// Hub is the shared rendezvous point Buses attached to it publish
// through. All Buses sharing a Hub receive each other's publishes;
// each Bus's own origin marker drops self-deliveries before the
// receiver fires.
//
// Hubs are safe for concurrent attachment + publish. Detach happens
// automatically on Bus.Close.
type Hub struct {
	mu      sync.RWMutex
	buses   map[string]*Bus // originID → bus
	closed  bool
}

// NewHub constructs an empty Hub. Hubs are cheap; create one per
// test cluster.
func NewHub() *Hub {
	return &Hub{buses: map[string]*Bus{}}
}

// attach registers a Bus with the Hub. The Hub fans every publish to
// every attached Bus's incoming queue.
func (h *Hub) attach(b *Bus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.buses[b.originID] = b
}

// detach removes a Bus from the Hub. Called by Bus.Close.
func (h *Hub) detach(originID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.buses, originID)
}

// publish fans the event to every attached Bus, including the
// publisher itself. Each Bus's receive loop drops messages whose
// origin matches its own ID (self-publish dedup).
func (h *Hub) publish(event events.Event, originID string) {
	h.mu.RLock()
	targets := make([]*Bus, 0, len(h.buses))
	for _, b := range h.buses {
		targets = append(targets, b)
	}
	h.mu.RUnlock()
	for _, b := range targets {
		select {
		case b.incoming <- incomingFrame{event: event, origin: originID}:
		default:
			// Drop on slow consumer — tests provision ample buffer
			// and a drop here is a real signal that the receiver
			// is overloaded or stuck.
		}
	}
}

// Close marks the Hub closed and detaches all buses. Idempotent.
// Safe to call concurrently with attach / publish.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.buses = map[string]*Bus{}
}

// Bus is the per-replica adapter. Implements events.Emitter so it
// can be wired directly as cfg.Emitter, and provides Subscribe / Run
// / Close lifecycle methods mirroring redisstore.Bus.
//
// Each Bus carries a per-instance random origin ID; messages it
// publishes to the shared Hub are tagged with this ID, and its own
// receive loop drops messages carrying the same ID before invoking
// the receiver.
type Bus struct {
	hub      *Hub
	originID string
	receiver server.NotificationRelayReceiver

	mu       sync.Mutex
	channels map[string]struct{} // event names this bus listens to
	incoming chan incomingFrame
	closed   bool
	closeCh  chan struct{}
}

type incomingFrame struct {
	event  events.Event
	origin string
}

// NewBus attaches a new Bus to the Hub. Returns an error when hub or
// receiver is nil.
func NewBus(hub *Hub, receiver server.NotificationRelayReceiver) (*Bus, error) {
	if hub == nil {
		return nil, errors.New("memorystore.NewBus: hub is required")
	}
	if receiver == nil {
		return nil, errors.New("memorystore.NewBus: receiver is required")
	}
	var idBuf [16]byte
	if _, err := rand.Read(idBuf[:]); err != nil {
		return nil, fmt.Errorf("memorystore.NewBus: origin id: %w", err)
	}
	b := &Bus{
		hub:      hub,
		originID: hex.EncodeToString(idBuf[:]),
		receiver: receiver,
		channels: map[string]struct{}{},
		incoming: make(chan incomingFrame, 1024),
		closeCh:  make(chan struct{}),
	}
	hub.attach(b)
	return b, nil
}

// Emit implements events.Emitter — publishes the event via the
// shared Hub. Origin marker handling is automatic.
func (b *Bus) Emit(_ context.Context, event events.Event) error {
	b.hub.publish(event, b.originID)
	return nil
}

// Subscribe declares the event names this Bus listens to. Calling
// Subscribe with a name already declared is a no-op.
func (b *Bus) Subscribe(_ context.Context, eventNames ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("memorystore.Bus: subscribe on closed bus")
	}
	for _, n := range eventNames {
		b.channels[n] = struct{}{}
	}
	return nil
}

// Run starts the receive loop. Blocks until ctx is cancelled or
// Close is called. Returns nil on clean shutdown.
func (b *Bus) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-b.closeCh:
			return nil
		case f, ok := <-b.incoming:
			if !ok {
				return nil
			}
			if f.origin == b.originID {
				continue
			}
			b.mu.Lock()
			_, subscribed := b.channels[f.event.Name]
			b.mu.Unlock()
			if !subscribed {
				continue
			}
			b.receiver.ReceiveRelay(ctx, EventMethodName, f.event)
		}
	}
}

// Close detaches the Bus from its Hub and signals the receive loop
// to exit. Idempotent.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.hub.detach(b.originID)
	close(b.closeCh)
	return nil
}

// Compile-time check: Bus satisfies events.Emitter.
var _ events.Emitter = (*Bus)(nil)
