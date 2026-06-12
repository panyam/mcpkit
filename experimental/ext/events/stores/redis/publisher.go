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
// Each Publisher instance has a process-unique OriginID that Emit
// stamps onto event.Meta[events.OriginMetaKey] (see ../../origin.go for
// the shared seam). A co-located Subscriber configured with the same
// value via Options.SkipOriginID drops those messages — the standard
// Pattern B requirement so a replica's own publishes don't re-fire its
// local fanout (yield-side already handled that in-process).
type Publisher struct {
	opts     Options
	originID string
}

// NewPublisher returns a Publisher that, when wired into events.Config.Emitter
// (typically composed with events.NewLocalEmitter — see the package doc's
// anti-loop pattern), broadcasts every yielded event to Redis.
//
// A 16-byte random origin ID is generated at construction time. Use
// (*Publisher).OriginID() to wire it into the co-located Subscriber's
// Options.SkipOriginID.
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

// OriginID returns this Publisher's process-unique origin marker. Wire
// it into the co-located Subscriber's Options.SkipOriginID so its
// receive path drops events this publisher emitted.
func (p *Publisher) OriginID() string { return p.originID }

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
	event.Meta = events.StampOriginIDOnMeta(event.Meta, p.originID)
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
