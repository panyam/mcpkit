package redisstore

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// sample is a convenience builder for an events.Event the tests can
// publish and compare against the round-tripped form.
func sample(name, payload string) events.Event {
	return events.Event{
		EventID:   "evt-" + name + "-1",
		Name:      name,
		Timestamp: "2026-06-09T20:00:00Z",
		Data:      json.RawMessage(`{"text":"` + payload + `"}`),
	}
}

// captureDeliver is a thread-safe sink for testing — records every
// event Subscriber.Run delivers, exposes a snapshot for assertions.
type captureDeliver struct {
	mu     sync.Mutex
	events []events.Event
}

func (c *captureDeliver) fn() DeliverFunc {
	return func(_ context.Context, e events.Event) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.events = append(c.events, e)
		return nil
	}
}

func (c *captureDeliver) snapshot() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]events.Event, len(c.events))
	copy(out, c.events)
	return out
}

// startSubscriber spins up a Subscriber wired to cap, runs it in a
// goroutine, waits for the SUBSCRIBE to land, and returns the
// subscriber + a stop func.
func startSubscriber(t *testing.T, opts Options, cap *captureDeliver, channels ...string) (*Subscriber, func()) {
	t.Helper()
	sub, err := NewSubscriber(opts, cap.fn())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, sub.Subscribe(ctx, channels...))
	done := make(chan struct{})
	go func() {
		_ = sub.Run(ctx)
		close(done)
	}()
	// Wait for the live PubSub to land — Subscribe before Run queues
	// channels; Run does the actual SUBSCRIBE. 50ms is plenty for
	// miniredis (single-digit ms) with headroom for real Redis.
	time.Sleep(50 * time.Millisecond)
	stop := func() {
		cancel()
		_ = sub.Close()
		<-done
	}
	return sub, stop
}

// TestRoundTrip_DeliversEventToSubscriber verifies the load-bearing
// path: Publisher.Emit on one side → Subscriber.Run delivers the same
// event payload on the other. Locks the at-most-once-but-each-delivered-
// message-is-intact contract.
func TestRoundTrip_DeliversEventToSubscriber(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	cap := &captureDeliver{}
	_, stop := startSubscriber(t, opts, cap, "chat.message")
	defer stop()

	pub, err := NewPublisher(opts)
	require.NoError(t, err)
	want := sample("chat.message", "hello")
	require.NoError(t, pub.Emit(t.Context(), want))

	require.Eventually(t, func() bool {
		return len(cap.snapshot()) == 1
	}, 2*time.Second, 10*time.Millisecond, "subscriber should receive exactly one event")

	got := cap.snapshot()[0]
	assert.Equal(t, want.EventID, got.EventID)
	assert.Equal(t, want.Name, got.Name)
	assert.JSONEq(t, string(want.Data), string(got.Data))
}

// TestChannelSeparation_OnlyMatchingChannelReceives verifies the
// channelization: a Subscriber listening only on "chat.message" must
// NOT receive a "presence.changed" event published on the bus.
// Catches the regression where a future refactor accidentally
// PSUBSCRIBE'd a wildcard.
func TestChannelSeparation_OnlyMatchingChannelReceives(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	cap := &captureDeliver{}
	_, stop := startSubscriber(t, opts, cap, "chat.message")
	defer stop()

	pub, err := NewPublisher(opts)
	require.NoError(t, err)
	require.NoError(t, pub.Emit(t.Context(), sample("presence.changed", "alice-online")))
	require.NoError(t, pub.Emit(t.Context(), sample("chat.message", "hi")))

	require.Eventually(t, func() bool {
		return len(cap.snapshot()) == 1
	}, 2*time.Second, 10*time.Millisecond)

	got := cap.snapshot()
	assert.Len(t, got, 1, "exactly the chat.message event")
	assert.Equal(t, "chat.message", got[0].Name)
}

// TestLateSubscriber_MissesPreviousMessages locks in the at-most-once
// contract documented on the package. A subscriber that joins AFTER
// a publish completes does NOT receive the missed message.
// (When this property changes — e.g., migration to Redis Streams —
// this test breaks loudly so the contract update is forced.)
func TestLateSubscriber_MissesPreviousMessages(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	pub, err := NewPublisher(opts)
	require.NoError(t, err)

	// Publish first, subscribe after — typical pubsub miss window.
	require.NoError(t, pub.Emit(t.Context(), sample("chat.message", "pre-sub")))

	cap := &captureDeliver{}
	_, stop := startSubscriber(t, opts, cap, "chat.message")
	defer stop()

	// Give it a beat in case anything was buffered (it shouldn't be).
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, cap.snapshot(), "late subscriber must miss the pre-subscribe message (at-most-once)")
}

// TestMultiSubscriber_FansIn verifies the SUBSCRIBE side's fan-in:
// two subscribers on the same channel both receive every published
// message. Models the multi-replica receive shape.
func TestMultiSubscriber_FansIn(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	capA, capB := &captureDeliver{}, &captureDeliver{}
	_, stopA := startSubscriber(t, opts, capA, "chat.message")
	defer stopA()
	_, stopB := startSubscriber(t, opts, capB, "chat.message")
	defer stopB()

	pub, err := NewPublisher(opts)
	require.NoError(t, err)
	require.NoError(t, pub.Emit(t.Context(), sample("chat.message", "fan-in")))

	require.Eventually(t, func() bool {
		return len(capA.snapshot()) == 1 && len(capB.snapshot()) == 1
	}, 2*time.Second, 10*time.Millisecond, "both subscribers should receive the message")
}

// fakeCodec is a Codec stand-in that prepends a tag so tests can prove
// the publisher used the configured codec (not a hardcoded JSON path).
type fakeCodec struct{}

func (fakeCodec) Encode(e events.Event) ([]byte, error) {
	body, err := JSONCodec{}.Encode(e)
	if err != nil {
		return nil, err
	}
	return append([]byte("FAKE:"), body...), nil
}

func (fakeCodec) Decode(b []byte) (events.Event, error) {
	if len(b) < 5 || string(b[:5]) != "FAKE:" {
		// Fall through — this codec only decodes what it encoded.
		return events.Event{}, assertWireMismatch
	}
	return JSONCodec{}.Decode(b[5:])
}

var assertWireMismatch = &codecMismatchErr{}

type codecMismatchErr struct{}

func (*codecMismatchErr) Error() string { return "fakeCodec: not a FAKE-prefixed payload" }

// TestCodecSwap_RoundTripsThroughCustomCodec proves the Codec
// interface is honored end-to-end: both publisher and subscriber use
// the configured codec; the JSONCodec default is NOT silently
// substituted.
func TestCodecSwap_RoundTripsThroughCustomCodec(t *testing.T) {
	opts := Options{Client: newTestClient(t), Codec: fakeCodec{}}
	cap := &captureDeliver{}
	_, stop := startSubscriber(t, opts, cap, "chat.message")
	defer stop()

	pub, err := NewPublisher(opts)
	require.NoError(t, err)
	want := sample("chat.message", "tagged")
	require.NoError(t, pub.Emit(t.Context(), want))

	require.Eventually(t, func() bool {
		return len(cap.snapshot()) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, want.EventID, cap.snapshot()[0].EventID)
}

// TestSubscriber_CleanShutdown verifies the shutdown contract: ctx
// cancel ends Run with nil; Close is idempotent.
func TestSubscriber_CleanShutdown(t *testing.T) {
	opts := Options{Client: newTestClient(t)}
	sub, err := NewSubscriber(opts, func(context.Context, events.Event) error { return nil })
	require.NoError(t, err)
	require.NoError(t, sub.Subscribe(t.Context(), "chat.message"))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- sub.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "ctx cancel should produce a nil-error Run exit")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}

	assert.NoError(t, sub.Close(), "Close after ctx cancel")
	assert.NoError(t, sub.Close(), "Close is idempotent")
}
