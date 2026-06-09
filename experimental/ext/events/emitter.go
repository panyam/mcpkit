package events

import (
	"context"

	"github.com/panyam/mcpkit/server"
)

// Emitter is the output-side seam: "given an event, deliver it." It
// is the dual of EventSource — Source produces events; Emitter
// consumes them. The default implementation (NewLocalEmitter) delivers
// to local SSE listeners (via Server.Broadcast) and to the local
// WebhookRegistry, matching today's single-replica behavior bit-for-
// bit. Multi-replica deployments compose alternative emitters via
// NewCompositeEmitter — e.g., a fanout emitter that POSTs to peer
// replicas' HTTPSource inject endpoints (which already exist as a
// public source pattern), or a Redis-backed emitter that publishes to
// a pubsub channel.
//
// Why an output interface (not a pub/sub bus): the asymmetry mirrors
// what the codebase already does on the input side. EventSource
// abstracts "where events come from"; Emitter abstracts "where they
// go." A pub/sub bus is one possible implementation of an Emitter
// (publishing to the bus is one fanout target), not a separate seam.
// Receive-side cross-replica reuses the existing HTTPSource pattern
// (PR 653) — no new "Subscribe" API is needed; replicas already
// accept incoming events via HTTPSource's /inject endpoint.
//
// API shape: ctx-first, error return. Errors are reported but do not
// halt further fanout — a composite continues delivering to remaining
// children after a child returns an error, and returns the first
// error it saw (or nil). This matches "at-least-once across child
// emitters" semantics; callers that want strict ordering can wrap
// individual emitters with retry logic.
//
// Concurrency: implementations MUST be safe for concurrent Emit
// calls. The default LocalEmitter and CompositeEmitter satisfy this
// because their underlying targets (Server.Broadcast, WebhookRegistry.Deliver)
// are already concurrent-safe.
type Emitter interface {
	// Emit delivers one event. Blocks until all of this emitter's
	// targets have accepted or rejected the event — the yield path
	// depends on this to preserve back-pressure semantics.
	Emit(ctx context.Context, event Event) error
}

// NewLocalEmitter returns the default in-process emitter: broadcasts
// to local SSE listeners via Server.Broadcast AND delivers to local
// webhook targets via WebhookRegistry.Deliver. Either srv or webhooks
// may be nil; a nil component is skipped. This matches the historical
// emit-hook body that ran inline in Register before the seam landed.
//
// Multi-replica deployments wrap this in a composite with additional
// targets (a peer-fanout emitter that POSTs to sibling replicas'
// HTTPSource inject endpoints, a Redis pubsub emitter, etc.).
func NewLocalEmitter(srv *server.Server, webhooks *WebhookRegistry) Emitter {
	return &localEmitter{srv: srv, webhooks: webhooks}
}

// NewCompositeEmitter returns an Emitter that fans Emit out to every
// child in order. Continues after a child returns an error so a slow
// or failing target does not silently drop deliveries to others;
// returns the first error encountered (or nil). Composition is the
// primary way to add cross-replica fanout without changing existing
// single-replica wiring.
//
// Pass no children to get a no-op emitter (Emit always returns nil).
// Children may themselves be composites — nesting works.
func NewCompositeEmitter(children ...Emitter) Emitter {
	return compositeEmitter(children)
}

// localEmitter wraps the public Emit + EmitToWebhooks functions so
// they're invocable through the Emitter interface. The public
// functions retain their existing signatures so external TypedSource
// authors continue to call them directly.
type localEmitter struct {
	srv      *server.Server
	webhooks *WebhookRegistry
}

func (e *localEmitter) Emit(ctx context.Context, event Event) error {
	if e.srv != nil {
		Emit(ctx, e.srv, event)
	}
	if e.webhooks != nil {
		EmitToWebhooks(ctx, e.webhooks, event)
	}
	return nil
}

// compositeEmitter is a Slice of children, fanning Emit to each in
// order. Defined as a type alias on the slice (rather than a struct
// holding a slice) so a zero-children value is well-defined and
// callers can construct via type assertion in tests.
type compositeEmitter []Emitter

func (c compositeEmitter) Emit(ctx context.Context, event Event) error {
	var firstErr error
	for _, child := range c {
		if child == nil {
			continue
		}
		if err := child.Emit(ctx, event); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
