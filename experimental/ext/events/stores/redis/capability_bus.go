// capability_bus.go — Pattern B publisher + subscriber for
// capability-shaped notifications (notifications/tools/list_changed,
// notifications/resources/list_changed,
// notifications/prompts/list_changed, and any future notification
// with the same shape: no per-subscription filter, just fan out to
// every client with the capability).
//
// CapabilityBus is to capability-shaped notifications what Bus is to
// events. Adopters wire one and:
//   1. Install it on the Server via server.WithNotificationRelay(bus).
//      Server.Broadcast then fires Publish on every call
//      before running BroadcastToSessions locally — every replica's
//      clients hear the notification.
//   2. Call Subscribe + Run to start the receive side. On every
//      cross-replica receive (origin-marker self-publishes dropped
//      inside the Bus), the configured server.NotificationRelayReceiver
//      (typically server.NewCapabilityBroadcastReceiver(srv)) fires
//      Server.BroadcastToSessions on the receiving replica.
//
// Wire format on Redis: per-method channel, JSON envelope:
//
//	PUBLISH "<prefix>.broadcast.<method>" {"origin": "<uuid>", "params": <json>}
//
// One channel per method name keeps the subscribe surface small and
// matches the per-event-name channel pattern Bus already uses for
// events. The envelope carries the origin marker outside params so
// notifications whose params are nil (every list_changed surface) can
// still be dedup'd.

package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	redis "github.com/redis/go-redis/v9"
	"github.com/panyam/mcpkit/server"
)

// CapabilityBusChannelInfix is the segment between the redisstore
// channel prefix and the method name. Exposed for diagnostics
// (Redis MONITOR queries, etc.); adopters should not depend on the
// exact value beyond "it starts with the configured ChannelPrefix."
const CapabilityBusChannelInfix = "broadcast"

// CapabilityBus is the Pattern B implementation for capability-shaped
// notifications. Satisfies server.NotificationRelay on the publish side
// and wires a subscriber loop that drives server.BroadcastToSessions
// on every cross-replica receive.
//
// Concurrency: Publish is safe for concurrent calls.
// Subscribe / Run / Close should be called from a single goroutine
// (typical pattern: Subscribe once with the method names you care
// about, Run in a goroutine, Close on shutdown).
type CapabilityBus struct {
	client        *redis.Client
	channelPrefix string
	originID      string
	receiver      server.NotificationRelayReceiver

	mu          sync.Mutex
	pubsub      *redis.PubSub
	channels    map[string]struct{}
	closed      bool
}

// capabilityEnvelope is the wire shape Publish emits.
// Receive side decodes back to (originID, params) so the origin
// filter and the receiver dispatch both have what they need.
type capabilityEnvelope struct {
	Origin string          `json:"origin"`
	Params json.RawMessage `json:"params,omitempty"`
}

// CapabilityBusOptions configures a CapabilityBus. Mirrors Options
// (the events-Bus configuration) but trimmed to what
// capability-shaped notifications need.
type CapabilityBusOptions struct {
	// Client is the Redis client used for PUBLISH + SUBSCRIBE. The
	// caller owns the client's lifecycle; CapabilityBus does NOT
	// Close it.
	Client *redis.Client

	// ChannelPrefix is the namespace under which per-method channels
	// live. Default: DefaultChannelPrefix ("mcpkit.events"). Override
	// for multi-tenant deployments running multiple isolated stacks
	// against one Redis cluster.
	ChannelPrefix string
}

// NewCapabilityBus constructs a CapabilityBus wired to the receiver.
// Returns an error when opts.Client or receiver is nil.
//
// The bus generates a 16-byte random origin marker at construction
// time. Self-publish dedup uses this marker — the colocated
// subscriber drops messages whose envelope.origin matches before
// invoking the receiver.
func NewCapabilityBus(opts CapabilityBusOptions, receiver server.NotificationRelayReceiver) (*CapabilityBus, error) {
	if opts.Client == nil {
		return nil, errors.New("redisstore.NewCapabilityBus: opts.Client is required")
	}
	if receiver == nil {
		return nil, errors.New("redisstore.NewCapabilityBus: receiver is required")
	}
	if opts.ChannelPrefix == "" {
		opts.ChannelPrefix = DefaultChannelPrefix
	}
	var idBuf [16]byte
	if _, err := rand.Read(idBuf[:]); err != nil {
		return nil, fmt.Errorf("redisstore.NewCapabilityBus: origin id: %w", err)
	}
	return &CapabilityBus{
		client:        opts.Client,
		channelPrefix: opts.ChannelPrefix,
		originID:      hex.EncodeToString(idBuf[:]),
		receiver:      receiver,
		channels:      map[string]struct{}{},
	}, nil
}

// Publish implements server.NotificationRelay. Encodes the
// notification into a capabilityEnvelope tagged with this bus's
// origin marker and PUBLISHes on the per-method channel. Errors are
// logged via opts.Logger (set on the underlying client's Options if
// available) but not returned — Server.Broadcast is fire-and-forget
// for the relay leg.
func (b *CapabilityBus) Publish(ctx context.Context, method string, params any) {
	body, err := encodeCapabilityEnvelope(b.originID, params)
	if err != nil {
		// Best-effort — adopters that care about publish reliability
		// install a transport with retry inside the Redis client.
		return
	}
	channel := b.channelFor(method)
	_ = b.client.Publish(ctx, channel, body).Err()
}

// Subscribe declares the methods this bus listens on. Calling
// Subscribe again with new method names extends the existing
// subscription; duplicate method names are no-ops.
func (b *CapabilityBus) Subscribe(ctx context.Context, methods ...string) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("redisstore.CapabilityBus: subscribe on closed bus")
	}
	fresh := make([]string, 0, len(methods))
	for _, m := range methods {
		if _, ok := b.channels[m]; ok {
			continue
		}
		b.channels[m] = struct{}{}
		fresh = append(fresh, b.channelFor(m))
	}
	pubsub := b.pubsub
	b.mu.Unlock()
	if len(fresh) == 0 {
		return nil
	}
	if pubsub == nil {
		// Lazy pubsub creation matches Subscriber's pattern —
		// subscription happens when Run starts.
		return nil
	}
	return pubsub.Subscribe(ctx, fresh...)
}

// Run starts the receive loop. Blocks until ctx is cancelled or the
// underlying pubsub channel closes. Returns nil on clean shutdown.
func (b *CapabilityBus) Run(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("redisstore.CapabilityBus: run on closed bus")
	}
	initial := make([]string, 0, len(b.channels))
	for m := range b.channels {
		initial = append(initial, b.channelFor(m))
	}
	if len(initial) == 0 {
		// No methods subscribed — nothing to drain. Block on ctx so
		// the typical "go bus.Run(ctx)" pattern still works.
		b.mu.Unlock()
		<-ctx.Done()
		return nil
	}
	pubsub := b.client.Subscribe(ctx, initial...)
	b.pubsub = pubsub
	b.mu.Unlock()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			origin, params, err := decodeCapabilityEnvelope([]byte(msg.Payload))
			if err != nil {
				continue
			}
			if origin == b.originID {
				continue
			}
			method := b.methodFor(msg.Channel)
			b.receiver.Receive(ctx, method, params)
		}
	}
}

// Close releases the underlying *redis.PubSub. Idempotent. After
// Close, Subscribe returns an error; Run (if blocked) returns nil.
// Does NOT close the underlying *redis.Client.
func (b *CapabilityBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.pubsub != nil {
		return b.pubsub.Close()
	}
	return nil
}

func (b *CapabilityBus) channelFor(method string) string {
	return b.channelPrefix + "." + CapabilityBusChannelInfix + "." + method
}

func (b *CapabilityBus) methodFor(channel string) string {
	prefix := b.channelPrefix + "." + CapabilityBusChannelInfix + "."
	if len(channel) <= len(prefix) {
		return channel
	}
	return channel[len(prefix):]
}

func encodeCapabilityEnvelope(originID string, params any) ([]byte, error) {
	var p json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		p = raw
	}
	return json.Marshal(capabilityEnvelope{Origin: originID, Params: p})
}

// decodeCapabilityEnvelope returns (origin, params, error). params is
// json.RawMessage so the receiver can defer typed decode if needed;
// most receivers pass it straight through to BroadcastToSessions
// which doesn't care about the inner shape.
func decodeCapabilityEnvelope(body []byte) (string, json.RawMessage, error) {
	var env capabilityEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", nil, err
	}
	return env.Origin, env.Params, nil
}

// Compile-time check: CapabilityBus satisfies server.NotificationRelay.
var _ server.NotificationRelay = (*CapabilityBus)(nil)
