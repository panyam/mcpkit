package redisstore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// DeliverFunc is the per-message callback wired into a Subscriber.
// Receives every decoded events.Event the subscriber pulls off Redis.
// MUST be wired to the LOCAL emitter only — passing
// events.Config.Emitter (which may itself contain a Publisher) would
// cause every received event to be re-PUBLISHed, looping forever.
//
// See the package doc's anti-loop pattern.
//
// Implementations SHOULD complete quickly; the subscriber goroutine
// processes messages sequentially. For expensive work, push the event
// onto a queue and process it elsewhere.
type DeliverFunc func(ctx context.Context, event events.Event) error

// Subscriber pulls messages off subscribed Redis channels, decodes
// them, and delivers each to a DeliverFunc. Subscribe adds channels;
// Run blocks until ctx is cancelled or a fatal error trips. Close
// releases the underlying *redis.PubSub.
type Subscriber struct {
	opts    Options
	deliver DeliverFunc

	mu       sync.Mutex
	pubsub   *redis.PubSub
	channels map[string]struct{}
	closed   bool
}

// NewSubscriber builds a Subscriber from Options and the LOCAL emit
// callback. Returns an error when Options.Client or deliver is nil.
// The returned Subscriber holds no Redis-side state until the first
// Subscribe call.
func NewSubscriber(opts Options, deliver DeliverFunc) (*Subscriber, error) {
	if opts.Client == nil {
		return nil, errors.New("redisstore.NewSubscriber: Options.Client is required")
	}
	if deliver == nil {
		return nil, errors.New("redisstore.NewSubscriber: deliver is required")
	}
	return &Subscriber{
		opts:     opts.withDefaults(),
		deliver:  deliver,
		channels: make(map[string]struct{}),
	}, nil
}

// Subscribe adds one or more event-name channels to the Subscriber's
// SUBSCRIBE set. Idempotent — re-subscribing to an existing channel
// is a no-op. May be called before or after Run; in the latter case,
// new channels are added to the live *redis.PubSub.
func (s *Subscriber) Subscribe(ctx context.Context, eventNames ...string) error {
	if len(eventNames) == 0 {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("redisstore: subscriber closed")
	}
	var fresh []string
	for _, name := range eventNames {
		ch := s.opts.channelFor(name)
		if _, exists := s.channels[ch]; exists {
			continue
		}
		s.channels[ch] = struct{}{}
		fresh = append(fresh, ch)
	}
	pubsub := s.pubsub
	s.mu.Unlock()
	if len(fresh) == 0 {
		return nil
	}
	if pubsub != nil {
		// Live — extend the existing subscription set.
		if err := pubsub.Subscribe(ctx, fresh...); err != nil {
			return fmt.Errorf("redisstore: SUBSCRIBE failed: %w", err)
		}
	}
	// Otherwise the channels are queued; Run will SUBSCRIBE on entry.
	return nil
}

// Run subscribes to every channel registered via Subscribe so far and
// reads messages until ctx is cancelled or the underlying *redis.PubSub
// closes. Each decoded event flows through DeliverFunc; decode errors
// are logged and the message dropped (poison-pill protection — one
// corrupt body MUST NOT take the goroutine down).
//
// Returns nil on clean shutdown (ctx cancelled), an error if Redis
// itself errors fatally. Auto-reconnect on transient Redis errors is
// handled by go-redis/v9's internal pubsub loop — a Redis restart
// produces a brief read pause, not a fatal Run-exit.
func (s *Subscriber) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("redisstore: subscriber closed")
	}
	if s.pubsub != nil {
		s.mu.Unlock()
		return errors.New("redisstore: Run called twice")
	}
	initial := make([]string, 0, len(s.channels))
	for ch := range s.channels {
		initial = append(initial, ch)
	}
	pubsub := s.opts.Client.Subscribe(ctx, initial...)
	s.pubsub = pubsub
	s.mu.Unlock()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				// Channel closed — usually because someone called
				// Close concurrently. Treat as clean shutdown.
				return nil
			}
			event, err := s.opts.Codec.Decode([]byte(msg.Payload))
			if err != nil {
				s.opts.Logger("redisstore: decode failed on channel %q: %v", msg.Channel, err)
				continue
			}
			// SEP-414 trace context propagation: stitch the
			// per-message ctx to the publisher-side span when the
			// event.Meta carried one. ExtractTraceContext returns a
			// zero TraceContext when Meta is absent / malformed; in
			// that case msgCtx equals ctx (no derivation happens
			// because WithTraceContext on a zero TC is a no-op).
			msgCtx := ctx
			if tc := mcpcore.ExtractTraceContext(event.Meta); !tc.IsZero() {
				msgCtx = mcpcore.WithTraceContext(ctx, tc)
			}
			if err := s.deliver(msgCtx, event); err != nil {
				s.opts.Logger("redisstore: deliver failed for event %q: %v", event.Name, err)
				// Per the Emitter contract, errors are reported but
				// do not halt further fanout. Same applies here —
				// continue draining the channel.
			}
		}
	}
}

// Close releases the underlying *redis.PubSub. Idempotent. After
// Close, Subscribe returns an error; Run (if still blocked) returns
// nil as the read channel drains.
func (s *Subscriber) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pubsub := s.pubsub
	s.pubsub = nil
	s.mu.Unlock()
	if pubsub != nil {
		return pubsub.Close()
	}
	return nil
}
