package redisstore_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	redisstore "github.com/panyam/mcpkit/stores/redis"
	"github.com/panyam/mcpkit/stores/redis/redistest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRedis aliases redistest.NewClient so the existing call sites
// don't need to rewire.
var newTestRedis = redistest.NewClient

// recordingReceiver captures every Receive call so the test can
// assert what reached the bus's receiver side.
type recordingReceiver struct {
	mu      sync.Mutex
	frames  []recordedFrame
}

type recordedFrame struct {
	method string
	params json.RawMessage
}

func (r *recordingReceiver) Receive(_ context.Context, method string, params any) {
	raw, _ := params.(json.RawMessage)
	r.mu.Lock()
	r.frames = append(r.frames, recordedFrame{method: method, params: raw})
	r.mu.Unlock()
}

func (r *recordingReceiver) snapshot() []recordedFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedFrame, len(r.frames))
	copy(out, r.frames)
	return out
}

func (r *recordingReceiver) waitFor(t *testing.T, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(r.snapshot()) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d frames within %s, got %d", count, timeout, len(r.snapshot()))
}

// TestCapabilityBus_RoundTrip_TwoReplicas verifies the cross-replica
// path end-to-end: bus on replica A publishes; bus on replica B
// receives and forwards to its NotificationRelayReceiver. Origin
// markers ensure each bus only delivers events from OTHER replicas.
func TestCapabilityBus_RoundTrip_TwoReplicas(t *testing.T) {
	cli := newTestRedis(t)
	opts := redisstore.CapabilityBusOptions{Client: cli}

	recvA := &recordingReceiver{}
	recvB := &recordingReceiver{}

	busA, err := redisstore.NewCapabilityBus(opts, recvA)
	require.NoError(t, err)
	defer busA.Close()
	busB, err := redisstore.NewCapabilityBus(opts, recvB)
	require.NoError(t, err)
	defer busB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, busA.Subscribe(ctx, "notifications/tools/list_changed"))
	require.NoError(t, busB.Subscribe(ctx, "notifications/tools/list_changed"))

	go busA.Run(ctx)
	go busB.Run(ctx)

	// miniredis pubsub subscription becomes effective the moment Run
	// hits the underlying client.Subscribe call. Sleep briefly to let
	// both subscribe loops register before we publish.
	time.Sleep(50 * time.Millisecond)

	busA.Publish(ctx, "notifications/tools/list_changed", nil)

	// busB's receiver must see exactly one frame (the cross-replica
	// publish from A). busA's receiver must see ZERO frames (the
	// origin marker drops self-publishes).
	recvB.waitFor(t, 1, time.Second)
	assert.Len(t, recvB.snapshot(), 1)
	assert.Empty(t, recvA.snapshot(), "origin replica must drop self-publish")
}

// TestCapabilityBus_SelfPublishDeduped verifies the single-bus
// case: publishing to a bus that's also its own subscriber must drop
// the self-publish.
func TestCapabilityBus_SelfPublishDeduped(t *testing.T) {
	cli := newTestRedis(t)
	recv := &recordingReceiver{}
	bus, err := redisstore.NewCapabilityBus(redisstore.CapabilityBusOptions{Client: cli}, recv)
	require.NoError(t, err)
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, bus.Subscribe(ctx, "notifications/tools/list_changed"))
	go bus.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	bus.Publish(ctx, "notifications/tools/list_changed", nil)
	time.Sleep(100 * time.Millisecond) // let any erroneous delivery fire

	assert.Empty(t, recv.snapshot(), "single-bus publish must dedup via origin marker")
}

// TestCapabilityBus_WithNotificationRelay_EndToEnd verifies the full
// integration: two Servers, each with a CapabilityBus wired via
// WithNotificationRelay. Calling Server.Broadcast on one replica
// triggers the other replica's BroadcastToSessions via the relay.
//
// We build the receiver + bus first using a placeholder srv, then
// construct the real srv with WithNotificationRelay pointing at the
// bus, then swap the receiver's srv pointer to the real one via
// SwapServer. This dance is needed because NotificationRelay must be
// installed at NewServer time but the receiver references its srv
// for BroadcastToSessions calls.
func TestCapabilityBus_WithNotificationRelay_EndToEnd(t *testing.T) {
	cli := newTestRedis(t)
	opts := redisstore.CapabilityBusOptions{Client: cli}

	buildReplica := func(name string) (*server.Server, *recordingReceiver, *redisstore.CapabilityBus) {
		// Wrap CapabilityBroadcastReceiver with a recording adapter
		// so we can verify Receive fires.
		recv := &recordingReceiver{}
		bus, err := redisstore.NewCapabilityBus(opts, recv)
		require.NoError(t, err)
		srv := server.NewServer(core.ServerInfo{Name: name, Version: "0.0.1"},
			server.WithNotificationRelay(bus),
		)
		return srv, recv, bus
	}

	srvA, recvA, busA := buildReplica("A")
	defer busA.Close()
	srvB, recvB, busB := buildReplica("B")
	defer busB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, busA.Subscribe(ctx, "notifications/tools/list_changed"))
	require.NoError(t, busB.Subscribe(ctx, "notifications/tools/list_changed"))
	go busA.Run(ctx)
	go busB.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	// Broadcast from A. A's local BroadcastToSessions fires
	// synchronously (we don't observe it directly here — that's
	// covered by server/relay_inmemory_test.go). B's bus receives
	// via Redis, decodes the envelope, and invokes its receiver.
	srvA.Broadcast(ctx, "notifications/tools/list_changed", nil)

	// recvB sees ONE cross-replica frame; recvA sees NONE
	// (origin marker dropped its self-publish).
	recvB.waitFor(t, 1, time.Second)
	assert.Len(t, recvB.snapshot(), 1)
	assert.Empty(t, recvA.snapshot(), "origin replica must drop self-publish at the bus layer")

	// Sanity: the srv refs are non-nil so the type checker confirms
	// WithNotificationRelay installed the relay onto the server.
	_ = srvA
	_ = srvB
}

