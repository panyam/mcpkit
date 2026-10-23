package eventsclient

import (
	"context"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// EventSink hands an event that has arrived to whatever acts on it for the
// user: a headless agent run when the user is away (#1579), a turn in their
// live session when they are present (#1581). SEP-3415 leaves this step open
// (Open Question 1), so the interface stays small enough for both.
//
// Deliver is called once per occurrence. Callers dedup before calling it (see
// Deduper), so an implementation need not. An error means the event was not
// acted on; whether to retry is the caller's choice.
type EventSink interface {
	Deliver(ctx context.Context, d Delivery) error
}

// SinkFunc adapts a function to EventSink.
type SinkFunc func(ctx context.Context, d Delivery) error

// Deliver calls f.
func (f SinkFunc) Deliver(ctx context.Context, d Delivery) error { return f(ctx, d) }

// Delivery is one event on its way to a sink.
//
// Event.Data is untrusted (SEP-3415 § Event payloads are untrusted data): it
// was written by whoever caused the event, not by the user. A sink that puts it
// in front of a model must present it as data, never as instructions, and
// should give that run only the tools the user's instruction needs.
type Delivery struct {
	// Server names the upstream events server the event came from, as the
	// caller configured it. Empty when there is only one.
	Server string

	// SubscriptionID identifies the subscription that matched, when the
	// delivery mode carries one (X-MCP-Subscription-Id on webhooks).
	SubscriptionID string

	// Instruction is what the user asked to happen when this event arrives.
	// Unlike Event.Data it comes from the user and is trusted.
	Instruction string

	// Session names the agent session that asked for this event, for a sink
	// that can continue that session rather than start a fresh one. Empty
	// when the watch was not tied to a session. SessionDir is the working
	// directory it ran in, for hosts that key sessions by directory (Claude
	// Code does).
	Session    string
	SessionDir string

	Event events.Event
}

// QualifiedName is the event's name prefixed with its server, as
// "<server>/<name>", or the bare name when Server is empty.
func (d Delivery) QualifiedName() string {
	if d.Server == "" {
		return d.Event.Name
	}
	return d.Server + "/" + d.Event.Name
}
