package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// Publisher implements events.Emitter by PUBLISHing each event onto a
// per-event-name Redis channel. Wire shape:
//
//	PUBLISH "<prefix>.<event.Name>" <Codec.Encode(event)>
//
// Stateless: no per-event subscription bookkeeping, no in-flight
// tracking. Emit is safe for concurrent use because the underlying
// *redis.Client is safe for concurrent use.
//
// Each Publisher instance has a process-unique origin marker (see
// origin.go). The marker is stamped on every published event so a
// colocated Subscriber wired through Bus can drop self-publishes. The
// marker is stripped from event.Meta on the receive side before
// deliverFn fires — adopters never see it.
type Publisher struct {
	opts     Options
	originID string
}

// NewPublisher returns a Publisher that, when wired into events.Config.Emitter
// (typically composed with events.NewLocalEmitter — see the package doc's
// anti-loop pattern), broadcasts every yielded event to Redis.
//
// A 16-byte random origin marker is generated at construction time and
// stamped on every published event. Adopters that want self-publish
// dedup against a colocated Subscriber should use Bus, which wires
// both ends with the marker known internally — Publisher's marker is
// not exposed.
//
// Returns an error when Options.Client is nil — every other field has
// a working default.
func NewPublisher(opts Options) (*Publisher, error) {
	if opts.Client == nil {
		return nil, errors.New("redisstore.NewPublisher: Options.Client is required")
	}
	var idBuf [16]byte
	if _, err := rand.Read(idBuf[:]); err != nil {
		return nil, fmt.Errorf("redisstore: origin id: %w", err)
	}
	return &Publisher{
		opts:     opts.withDefaults(),
		originID: hex.EncodeToString(idBuf[:]),
	}, nil
}

// Emit encodes the event via Options.Codec and PUBLISHes it to the
// per-event-name channel. Returns the codec error on Encode failure
// or the redis error on PUBLISH failure. A nil-but-no-subscribers
// PUBLISH (Redis returns the receiver count, zero is fine) is NOT an
// error — late subscribers missing messages is the documented
// at-most-once contract.
//
// SEP-414 trace context propagation: if ctx carries a TraceContext
// (set by core.WithTraceContext or the server's trace middleware),
// Emit stamps the W3C `traceparent` / `tracestate` keys onto
// event.Meta so the subscriber side can stitch its delivery span to
// the publisher's span via core.ExtractTraceContext. Caller-set
// values on event.Meta win — explicit traceparent on the event is
// never clobbered (mirrors core.InjectTraceContextIntoParams).
func (p *Publisher) Emit(ctx context.Context, event events.Event) error {
	event.Meta = injectTraceContext(ctx, event.Meta)
	event.Meta = stampOriginIDOnMeta(event.Meta, p.originID)
	body, err := p.opts.Codec.Encode(event)
	if err != nil {
		return fmt.Errorf("redisstore: encode failed: %w", err)
	}
	channel := p.opts.channelFor(event.Name)
	if err := p.opts.Client.Publish(ctx, channel, body).Err(); err != nil {
		return fmt.Errorf("redisstore: PUBLISH %s failed: %w", channel, err)
	}
	return nil
}

// injectTraceContext returns a Meta map that carries the inbound
// ctx's TraceContext under the W3C bare-name keys. Returns the input
// Meta unchanged when ctx has no TraceContext or when Meta already
// carries an explicit traceparent (caller-set wins).
//
// Always returns a freshly-allocated map when injection happens, so
// the caller's Event.Meta is never mutated through aliasing — events
// are passed by value but Meta is a reference type.
func injectTraceContext(ctx context.Context, meta map[string]any) map[string]any {
	tc := mcpcore.TraceContextFromContext(ctx)
	if tc.IsZero() {
		return meta
	}
	if _, callerSet := meta[mcpcore.MetaKeyTraceparent]; callerSet {
		return meta
	}
	cp := make(map[string]any, len(meta)+2)
	for k, v := range meta {
		cp[k] = v
	}
	mcpcore.InjectTraceContext(cp, tc)
	return cp
}

// Compile-time check that *Publisher satisfies events.Emitter — if
// the seam contract grows, this fails to compile at the right place.
var _ events.Emitter = (*Publisher)(nil)
