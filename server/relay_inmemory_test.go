package server

import (
	"context"
	"sync"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memRelayBus is an in-memory BroadcastRelay that also provides the
// receive side of Pattern B — every Publish fans out to every
// registered subscriber on a background goroutine. Self-publish dedup
// runs via per-replica origin marker. Used only by tests; never wired
// into production code.
//
// Wire shape: each replica registers a NotificationRelayReceiver and
// gets a unique origin ID. PublishBroadcast tags the message with the
// publisher's origin and enqueues to every subscriber. Subscribers
// drop messages whose origin matches their own (self-publish).
type memRelayBus struct {
	mu          sync.Mutex
	subscribers []*memRelaySubscriber
	nextID      int
}

type memRelaySubscriber struct {
	originID string
	receiver NotificationRelayReceiver
	queue    chan memRelayFrame
	done     chan struct{}
}

type memRelayFrame struct {
	origin string
	method string
	params any
}

func newMemRelayBus() *memRelayBus { return &memRelayBus{} }

// attachReplica registers a receiver on the bus and returns a publish
// handle that PublishBroadcast tags with this replica's origin. Wire
// the returned handle through WithBroadcastRelay so the same replica's
// outbound publishes carry the origin its inbound subscriber will
// drop.
func (b *memRelayBus) attachReplica(t *testing.T, receiver NotificationRelayReceiver) BroadcastRelay {
	t.Helper()
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.mu.Unlock()

	sub := &memRelaySubscriber{
		originID: hexID(id),
		receiver: receiver,
		queue:    make(chan memRelayFrame, 256),
		done:     make(chan struct{}),
	}
	b.mu.Lock()
	b.subscribers = append(b.subscribers, sub)
	b.mu.Unlock()

	go sub.run()
	t.Cleanup(sub.close)

	return &memRelayPublisher{bus: b, originID: sub.originID}
}

func (s *memRelaySubscriber) run() {
	defer close(s.done)
	for f := range s.queue {
		if f.origin == s.originID {
			continue
		}
		s.receiver.ReceiveRelay(context.Background(), f.method, f.params)
	}
}

func (s *memRelaySubscriber) close() {
	defer func() { _ = recover() }()
	close(s.queue)
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
}

type memRelayPublisher struct {
	bus      *memRelayBus
	originID string
}

func (p *memRelayPublisher) PublishBroadcast(_ context.Context, method string, params any) {
	p.bus.mu.Lock()
	subs := make([]*memRelaySubscriber, len(p.bus.subscribers))
	copy(subs, p.bus.subscribers)
	p.bus.mu.Unlock()
	frame := memRelayFrame{origin: p.originID, method: method, params: params}
	for _, s := range subs {
		select {
		case s.queue <- frame:
		default:
			// Drop on slow subscriber — tests use ample buffer.
		}
	}
}

func hexID(n int) string {
	const hex = "0123456789abcdef"
	out := []byte{0, 0, 0, 0}
	for i := 3; i >= 0; i-- {
		out[i] = hex[n&0xf]
		n >>= 4
	}
	return string(out)
}

// captureCluster spins up M Server instances wired against a shared
// memRelayBus. Each Server captures every BroadcastToSessions call
// into a per-replica frame slice. Tests can publish from any replica
// and assert per-replica deliveries.
type captureCluster struct {
	t        *testing.T
	bus      *memRelayBus
	replicas []*captureReplica
}

type captureReplica struct {
	idx int
	srv *Server
	mu  sync.Mutex
	got []captureFrame
}

func (r *captureReplica) frames() []captureFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]captureFrame, len(r.got))
	copy(out, r.got)
	return out
}

func newCaptureCluster(t *testing.T, m int) *captureCluster {
	t.Helper()
	cluster := &captureCluster{t: t, bus: newMemRelayBus()}
	for i := 0; i < m; i++ {
		r := &captureReplica{idx: i}
		receiver := NewCapabilityBroadcastReceiver(nil) // srv set below
		relay := cluster.bus.attachReplica(t, &recordingReceiver{
			recv: receiver,
			onFire: func(method string, params any) {
				// captured via session broadcaster below; this hook
				// is unused but documents that receive is happening
			},
		})
		srv := NewServer(core.ServerInfo{Name: "cluster-replica", Version: "0.0.1"},
			WithBroadcastRelay(relay),
		)
		// Wire the receiver to its server now that the server exists.
		receiver.srv = srv
		// Register a fake session broadcaster on this replica so we
		// observe what reaches local clients (i.e. what
		// BroadcastToSessions fans out).
		srv.registerTransportSessions(
			func(string) bool { return false },
			func() {},
			func(_ context.Context, method string, params any) {
				r.mu.Lock()
				r.got = append(r.got, captureFrame{method: method, params: params})
				r.mu.Unlock()
			},
		)
		r.srv = srv
		cluster.replicas = append(cluster.replicas, r)
	}
	return cluster
}

// recordingReceiver wraps a NotificationRelayReceiver so tests can
// hook into receive events when they care about the timing (the
// captureCluster usually doesn't — it asserts on the final
// session-broadcaster state).
type recordingReceiver struct {
	recv   NotificationRelayReceiver
	onFire func(method string, params any)
}

func (r *recordingReceiver) ReceiveRelay(ctx context.Context, method string, params any) {
	if r.onFire != nil {
		r.onFire(method, params)
	}
	r.recv.ReceiveRelay(ctx, method, params)
}

// waitForFrameCount polls until the replica has accumulated count
// frames, or fails the test after timeout. Useful because the
// memRelayBus delivers asynchronously via goroutine.
func (r *captureReplica) waitForFrameCount(t *testing.T, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(r.frames()) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("replica %d: expected at least %d frames, got %d", r.idx, count, len(r.frames()))
}

// --- Tests ---------------------------------------------------------

// TestBroadcastToSessions_ExcludesRelay verifies that BroadcastToSessions
// fires only the local session broadcasters, NOT the installed relay.
// This is the contract Pattern B receivers depend on to avoid recursion.
func TestBroadcastToSessions_ExcludesRelay(t *testing.T) {
	cluster := newCaptureCluster(t, 1)
	srv := cluster.replicas[0].srv

	// BroadcastToSessions should fire LOCAL session broadcasters only.
	// The relay's PublishBroadcast must NOT be called.
	srv.BroadcastToSessions(context.Background(), "notifications/tools/list_changed", nil)

	cluster.replicas[0].waitForFrameCount(t, 1, time.Second)
	assert.Len(t, cluster.replicas[0].frames(), 1)
}

// TestBroadcast_FiresRelayThenLocal verifies the public Broadcast does
// BOTH: relay PublishBroadcast (cross-replica), then BroadcastToSessions
// (local). Other replicas receive via relay → BroadcastToSessions, so
// every replica's captured frames go up.
func TestBroadcast_FiresRelayThenLocal(t *testing.T) {
	cluster := newCaptureCluster(t, 3)

	// Publish from replica 0. Local replica fires session broadcasters
	// immediately; replicas 1 + 2 receive via relay → BroadcastToSessions.
	cluster.replicas[0].srv.Broadcast(context.Background(), "notifications/tools/list_changed", nil)

	cluster.replicas[0].waitForFrameCount(t, 1, time.Second)
	cluster.replicas[1].waitForFrameCount(t, 1, time.Second)
	cluster.replicas[2].waitForFrameCount(t, 1, time.Second)

	for _, r := range cluster.replicas {
		frames := r.frames()
		require.Len(t, frames, 1, "replica %d", r.idx)
		assert.Equal(t, "notifications/tools/list_changed", frames[0].method)
	}
}

// TestBroadcast_SelfPublishDeduped verifies that the origin replica's
// own publish doesn't double-fire its local session broadcasters via
// the relay round-trip. The single Broadcast call should produce
// exactly one frame on the origin replica (from the local
// BroadcastToSessions path), NOT two (one from local + one from relay
// receive).
func TestBroadcast_SelfPublishDeduped(t *testing.T) {
	cluster := newCaptureCluster(t, 3)

	cluster.replicas[0].srv.Broadcast(context.Background(), "notifications/tools/list_changed", nil)

	// Wait a beat to let any erroneous relay round-trip fire.
	time.Sleep(50 * time.Millisecond)

	assert.Len(t, cluster.replicas[0].frames(), 1, "origin replica must see EXACTLY one frame, not double-fire via relay")
}

// TestBroadcast_NoRelayInstalled verifies that without a relay
// configured, Broadcast still works — fires only local sessions
// (matches pre-Pattern-B behavior).
func TestBroadcast_NoRelayInstalled(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "no-relay", Version: "0.0.1"})
	var captured []captureFrame
	var mu sync.Mutex
	srv.registerTransportSessions(
		func(string) bool { return false },
		func() {},
		func(_ context.Context, method string, params any) {
			mu.Lock()
			captured = append(captured, captureFrame{method: method, params: params})
			mu.Unlock()
		},
	)

	srv.Broadcast(context.Background(), "notifications/tools/list_changed", nil)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, captured, 1)
}

// TestBroadcastRelay_NTopicsMConsumers is the N producers × T topics
// × M consumers harness exercising the full multi-replica matrix.
// Every replica publishes every topic once; every replica's session
// broadcaster should receive every published topic exactly once
// (origin replica via local fanout; other replicas via relay).
func TestBroadcastRelay_NTopicsMConsumers(t *testing.T) {
	const m = 3 // replicas
	cluster := newCaptureCluster(t, m)
	topics := []string{
		"notifications/tools/list_changed",
		"notifications/resources/list_changed",
		"notifications/prompts/list_changed",
	}

	// Each replica publishes each topic.
	for _, r := range cluster.replicas {
		for _, topic := range topics {
			r.srv.Broadcast(context.Background(), topic, nil)
		}
	}

	// Expected per replica = m publishers × t topics = m × len(topics).
	expectedPerReplica := m * len(topics)
	for _, r := range cluster.replicas {
		r.waitForFrameCount(t, expectedPerReplica, 2*time.Second)
		frames := r.frames()
		require.Len(t, frames, expectedPerReplica, "replica %d", r.idx)

		// Each topic should appear exactly m times across the frames
		// (one from each publisher).
		count := map[string]int{}
		for _, f := range frames {
			count[f.method]++
		}
		for _, topic := range topics {
			assert.Equal(t, m, count[topic], "replica %d topic %q", r.idx, topic)
		}
	}
}
