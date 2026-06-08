package events

import (
	"context"
	"sync"
)

// EventBus is the cross-replica event-fanout seam. The default
// in-process implementation (NewInProcessEventBus) matches today's
// single-replica behavior bit-for-bit; multi-replica deployments
// plug in pub/sub backends (a Redis implementation lands in issue
// 634, follow-ups for Kafka / NATS land separately if a real
// consumer asks) so an event yielded on replica A reaches every
// replica's SSE listeners and webhook subscribers.
//
// Filter-on-receive (not filter-on-subscribe). SubscribeEvents
// filters by EventName only; events.Register installs ONE
// subscriber per concern (push, webhook) — not one per local
// subscription. Every replica receives every event for any event-
// name it has at least one local subscriber for; the per-subscriber
// EventDef.Match hook filters at delivery time on the receiving
// replica. Rationale:
//
//   - The Match hook ALREADY filters per (event × local subscriber)
//     today; bus granularity does not change correctness.
//   - EventName-only Subscribe maps cleanly to any pubsub primitive
//     (Redis pubsub channel-per-name, Kafka topic-per-name, NATS
//     subject-per-name). Fine-grained subscription would require
//     either topic explosion or a custom predicate language.
//   - Sub/unsub races during yield are avoided — replicas don't
//     register/deregister against the bus on every local subscribe.
//
// At massive scales (100s of replicas, sparse subscription matrices)
// the trade flips. If a real consumer reaches that point, a
// fine-grained variant lands as a sibling interface.
//
// API shape: every method follows the gRPC-style
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// convention pinned in STORAGE_SEAMS.md. ctx threads cancellation,
// deadlines, and trace context — the EventBus is the third
// SEP-414 propagation surface (a trace started on the yielding
// replica continues on the receiving replica via the ctx attached
// to the publish), in addition to the MCP wire and the HTTP header
// bridge.
//
// Concurrency contract: PublishEvent runs subscribers synchronously
// in registration order. Subscribers MUST NOT block indefinitely —
// the publisher's goroutine (typically the source's yield path)
// blocks until every subscriber's OnEvent returns. Subscribers that
// need to do slow work (HTTP POST, durable enqueue) should push it
// to a goroutine inside OnEvent.
//
// Cross-replica guarantees, by impl:
//
//   - At-least-once across replicas: every published event reaches
//     every replica's subscribers at least once.
//   - Per-subscription exactly-once: NOT guaranteed by the bus
//     alone. Subscribers handle dedup — for example, the webhook
//     dedup-across-replicas concern (which replica gets to POST a
//     given webhook target?) is a follow-up.
//   - Ordering within a replica: preserved (in-process bus runs
//     subscribers in registration order).
//   - Ordering across replicas: best-effort; bus-implementation
//     dependent.
type EventBus interface {
	// PublishEvent delivers an event to every matching subscriber.
	// Blocks until every subscriber's OnEvent has returned (for
	// pubsub backends, "matching subscriber" includes all peer
	// replicas; remote OnEvent runs after Redis/Kafka/etc. delivery).
	PublishEvent(ctx context.Context, req PublishEventRequest) (PublishEventResponse, error)

	// SubscribeEvents registers a synchronous handler for events
	// matching req.EventName. Returns a Subscription handle whose
	// Close removes the registration. The returned Subscription
	// MUST be closed when no longer needed; abandoned subscriptions
	// leak the handler closure and (for pubsub backends) the
	// underlying connection.
	SubscribeEvents(ctx context.Context, req SubscribeEventsRequest) (EventBusSubscription, error)
}

// PublishEventRequest carries one event into the bus.
type PublishEventRequest struct {
	Event Event
}

// PublishEventResponse is empty today; reserved for future fields
// (delivery counts, dedup keys).
type PublishEventResponse struct{}

// SubscribeEventsRequest configures a new bus subscription.
type SubscribeEventsRequest struct {
	// EventName filters this subscription to events with this Name.
	// Empty string subscribes to ALL events regardless of Name —
	// useful for diagnostic / tracing subscribers.
	EventName string
	// OnEvent fires synchronously per published event. The ctx is
	// the publisher's ctx (or its derived trace context, when the
	// bus impl threads ctx across the network hop). Long-running
	// handlers serialize the publisher's goroutine; push expensive
	// work to a separate goroutine inside.
	OnEvent func(ctx context.Context, event Event)
}

// EventBusSubscription is the handle returned by SubscribeEvents.
// Close must be called to unsubscribe; double-Close is a no-op.
type EventBusSubscription interface {
	// Close unsubscribes. After Close returns, the registered
	// OnEvent is guaranteed not to fire for any subsequent Publish.
	// Calling Close on a closed subscription is a silent no-op.
	Close() error
}

// NewInProcessEventBus returns the default in-process EventBus.
// PublishEvent fans out synchronously to subscribers in registration
// order; subscribers run on the publisher's goroutine. Suitable for
// single-process deployments and as the default when Config.EventBus
// is not configured. Multi-replica deployments plug in a shared
// backend.
func NewInProcessEventBus() EventBus {
	return &inProcessEventBus{}
}

// inProcessEventBus is the default EventBus. Holds subscribers under a
// RWMutex; PublishEvent takes the read lock so concurrent publishes
// don't serialize on subscriber list reads.
type inProcessEventBus struct {
	mu          sync.RWMutex
	subscribers []*inProcessSubscription
}

// inProcessSubscription is one registered handler. Holds a back-pointer
// to the bus so Close can remove itself from the bus's slice without
// the caller having to thread a registration index.
type inProcessSubscription struct {
	bus       *inProcessEventBus
	eventName string
	onEvent   func(ctx context.Context, event Event)
	closed    bool
}

func (b *inProcessEventBus) PublishEvent(ctx context.Context, req PublishEventRequest) (PublishEventResponse, error) {
	// RLock for the read; release before invoking handlers so a
	// re-entrant SubscribeEvents from inside a handler doesn't
	// deadlock. Snapshot the slice — handlers may call Close on
	// themselves mid-publish.
	b.mu.RLock()
	snapshot := make([]*inProcessSubscription, len(b.subscribers))
	copy(snapshot, b.subscribers)
	b.mu.RUnlock()

	for _, sub := range snapshot {
		if sub.closed {
			continue
		}
		if sub.eventName != "" && sub.eventName != req.Event.Name {
			continue
		}
		sub.onEvent(ctx, req.Event)
	}
	return PublishEventResponse{}, nil
}

func (b *inProcessEventBus) SubscribeEvents(_ context.Context, req SubscribeEventsRequest) (EventBusSubscription, error) {
	sub := &inProcessSubscription{
		bus:       b,
		eventName: req.EventName,
		onEvent:   req.OnEvent,
	}
	b.mu.Lock()
	b.subscribers = append(b.subscribers, sub)
	b.mu.Unlock()
	return sub, nil
}

// Close implements EventBusSubscription. Removes this subscription
// from the bus's subscriber list under the bus's lock. Idempotent.
func (s *inProcessSubscription) Close() error {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	// Remove from the bus's slice. Linear scan is fine — Close is
	// called at subscription teardown, not on the hot path.
	for i, existing := range s.bus.subscribers {
		if existing == s {
			s.bus.subscribers = append(s.bus.subscribers[:i], s.bus.subscribers[i+1:]...)
			break
		}
	}
	return nil
}
