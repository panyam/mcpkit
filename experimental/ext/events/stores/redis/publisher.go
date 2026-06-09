package redisstore

import (
	"context"
	"errors"
	"fmt"

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
type Publisher struct {
	opts Options
}

// NewPublisher returns a Publisher that, when wired into events.Config.Emitter
// (typically composed with events.NewLocalEmitter — see the package doc's
// anti-loop pattern), broadcasts every yielded event to Redis.
//
// Returns an error when Options.Client is nil — every other field has
// a working default.
func NewPublisher(opts Options) (*Publisher, error) {
	if opts.Client == nil {
		return nil, errors.New("redisstore.NewPublisher: Options.Client is required")
	}
	return &Publisher{opts: opts.withDefaults()}, nil
}

// Emit encodes the event via Options.Codec and PUBLISHes it to the
// per-event-name channel. Returns the codec error on Encode failure
// or the redis error on PUBLISH failure. A nil-but-no-subscribers
// PUBLISH (Redis returns the receiver count, zero is fine) is NOT an
// error — late subscribers missing messages is the documented
// at-most-once contract.
func (p *Publisher) Emit(ctx context.Context, event events.Event) error {
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

// Compile-time check that *Publisher satisfies events.Emitter — if
// the seam contract grows, this fails to compile at the right place.
var _ events.Emitter = (*Publisher)(nil)
