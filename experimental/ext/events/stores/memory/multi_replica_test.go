package memorystore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	memorystore "github.com/panyam/mcpkit/experimental/ext/events/stores/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Harness -------------------------------------------------------

// replica bundles a YieldingSource + its Bus and tracks which events
// reached its local subscriber slot. One replica per simulated
// process; multiple replicas share a Hub to simulate cross-process
// pub/sub.
type replica struct {
	idx     int
	src     *events.YieldingSource[payload]
	yield   func(context.Context, payload) error
	bus     *memorystore.Bus
	mu      sync.Mutex
	got     []events.Event
}

type payload struct {
	Tenant string `json:"tenant"`
	Body   string `json:"body"`
}

// startSubscriber registers a slot on this replica's source that
// captures every event passing per-slot Match. principal is the
// SubscribeOpts principal — used so tenant-based Match can run.
func (r *replica) startSubscriber(t *testing.T, principal string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	evCh, _ := r.src.Subscribe(ctx, events.SubscribeOpts{
		Principal:      principal,
		SubscriptionID: fmt.Sprintf("sub-%d-%s", r.idx, principal),
	})
	go func() {
		for se := range evCh {
			if se.Event.EventID == "" {
				continue
			}
			r.mu.Lock()
			r.got = append(r.got, se.Event)
			r.mu.Unlock()
		}
	}()
}

func (r *replica) snapshot() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.Event, len(r.got))
	copy(out, r.got)
	return out
}

// cluster bundles N replicas wired through a shared Hub. Every
// replica's bus subscribes to the same set of event names; per-slot
// Match decides who actually receives each yield.
type cluster struct {
	hub      *memorystore.Hub
	replicas []*replica
}

func newCluster(t *testing.T, n int, def events.EventDef, eventNames ...string) *cluster {
	t.Helper()
	if len(eventNames) == 0 {
		eventNames = []string{def.Name}
	}
	hub := memorystore.NewHub()
	t.Cleanup(hub.Close)
	c := &cluster{hub: hub}
	for i := 0; i < n; i++ {
		src, yield := events.NewYieldingSource[payload](def)
		bus, err := memorystore.NewBus(hub, src)
		require.NoError(t, err)
		// Wire the bus as the source's emit hook so yields publish
		// cross-replica via the in-memory hub. Without this the
		// for-loop fires LOCAL slots only and cross-replica
		// delivery never happens — same wiring shape adopters use
		// with redisstore.Bus.
		src.SetEmitHook(func(ctx context.Context, ev events.Event) {
			_ = bus.Emit(ctx, ev)
		})
		t.Cleanup(func() { _ = bus.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		require.NoError(t, bus.Subscribe(ctx, eventNames...))
		go func() { _ = bus.Run(ctx) }()
		c.replicas = append(c.replicas, &replica{idx: i, src: src, yield: yield, bus: bus})
	}
	// Give Run goroutines a beat to enter their loop.
	time.Sleep(20 * time.Millisecond)
	return c
}

func (c *cluster) waitForFrameCount(t *testing.T, r *replica, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(r.snapshot()) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("replica %d: expected %d events within %s, got %d", r.idx, want, timeout, len(r.snapshot()))
}

// payloadFor builds a JSON-encoded tenant-tagged payload.
func payloadFor(tenant, body string) payload {
	return payload{Tenant: tenant, Body: body}
}

// tenantMatch is the per-slot filter that gates by payload tenant
// matching the subscription's principal. Mirrors the demo's
// event-server tenantMatchFunc shape but smaller.
func tenantMatch(ctx events.HookContext, event events.Event, _ map[string]any) bool {
	var p payload
	if err := json.Unmarshal(event.Data, &p); err != nil {
		return false
	}
	if p.Tenant == "" {
		return true
	}
	return p.Tenant == ctx.Principal()
}

// --- Tests ---------------------------------------------------------

// TestMultiReplica_TenantScopingCrossReplica is the headline scenario.
// asgard streamer is on K1; babylon streamer is on K2; both
// subscribed to chat.message. A chat.message tagged for asgard
// yielded on K3 should reach ONLY K1's streamer, never K2's.
//
// This is the bug that motivated issue 755 — broadcasting the event
// without per-slot Match leaked asgard events to babylon streamers.
// With LocalDeliver routing the event through each replica's slot
// loop, Match runs per-slot and tenant scoping holds.
func TestMultiReplica_TenantScopingCrossReplica(t *testing.T) {
	def := events.EventDef{
		Name:        "chat.message",
		Description: "tenant-scoped chat",
		Delivery:    []string{"push"},
		Match:       tenantMatch,
	}
	c := newCluster(t, 3, def)

	// K1 hosts asgard streamer; K2 hosts babylon; K3 has no streamer.
	c.replicas[0].startSubscriber(t, "asgard")
	c.replicas[1].startSubscriber(t, "babylon")

	// Yield an asgard-tagged chat.message on K3 (no local streamer).
	data, _ := json.Marshal(payloadFor("asgard", "hi from asgard"))
	require.NoError(t, c.replicas[2].yield(context.Background(), payloadFor("asgard", "hi from asgard")))
	_ = data

	c.waitForFrameCount(t, c.replicas[0], 1, time.Second)
	assert.Empty(t, c.replicas[1].snapshot(),
		"babylon streamer must NOT receive asgard event — Match runs per slot on every replica")
	// Origin replica had no slots, so no delivery there.
	assert.Empty(t, c.replicas[2].snapshot())
}

// TestMultiReplica_SelfPublishDeduped — yield on the same replica
// where the subscriber lives. The for-loop delivers locally; the
// Bus's own subscribe-loop must drop the round-tripped copy.
func TestMultiReplica_SelfPublishDeduped(t *testing.T) {
	def := events.EventDef{
		Name:        "chat.message",
		Description: "self-publish dedup",
		Delivery:    []string{"push"},
	}
	c := newCluster(t, 3, def)
	c.replicas[0].startSubscriber(t, "")

	require.NoError(t, c.replicas[0].yield(context.Background(), payloadFor("", "x")))

	// Wait a beat for any erroneous round-trip to fire.
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, c.replicas[0].snapshot(), 1,
		"local yield must fire exactly ONCE — for-loop delivered, Bus must drop self-publish")
}

// TestMultiReplica_NProducersTConsumersMTopics — the broad coverage
// scenario the user asked for. N=3 producers (replicas) each yield
// T=3 different tenants on each of M=3 event types; subscribers per
// tenant across replicas should receive exactly the events tagged
// for THEIR tenant.
func TestMultiReplica_NProducersTConsumersMTopics(t *testing.T) {
	const replicaCount = 3
	tenants := []string{"asgard", "babylon", "camelot"}
	eventNames := []string{"chat.message", "presence.changed", "alert.fired"}

	// Build one cluster per event type since YieldingSource is
	// per-type. Each cluster shares replicas[i] across all event
	// types in the same logical replica — but for this test, having
	// per-type clusters is structurally simpler and the behavior we
	// care about (per-slot Match cross-replica) is the same.
	for _, eventName := range eventNames {
		t.Run(eventName, func(t *testing.T) {
			def := events.EventDef{
				Name:        eventName,
				Description: "multi-tenant",
				Delivery:    []string{"push"},
				Match:       tenantMatch,
			}
			c := newCluster(t, replicaCount, def)
			// Each replica hosts one streamer per tenant.
			for _, r := range c.replicas {
				for _, tenant := range tenants {
					r.startSubscriber(t, tenant)
				}
			}

			// Every replica publishes one event per tenant.
			for _, r := range c.replicas {
				for _, tenant := range tenants {
					require.NoError(t, r.yield(context.Background(),
						payloadFor(tenant, fmt.Sprintf("from-r%d-%s", r.idx, tenant))))
				}
			}

			// Each replica's per-tenant slot should see EXACTLY N
			// events — one from each replica's yield for that
			// tenant. Match drops other tenants' events.
			for _, r := range c.replicas {
				c.waitForFrameCount(t, r, replicaCount*len(tenants), 2*time.Second)
				gotByTenant := map[string]int{}
				for _, ev := range r.snapshot() {
					var p payload
					_ = json.Unmarshal(ev.Data, &p)
					gotByTenant[p.Tenant]++
				}
				// Each tenant on this replica has 1 subscriber slot;
				// the slot receives N events tagged for its tenant
				// (one from each replica's publish for that tenant).
				for _, tenant := range tenants {
					assert.Equal(t, replicaCount, gotByTenant[tenant],
						"replica %d tenant %q: want %d, got %d",
						r.idx, tenant, replicaCount, gotByTenant[tenant])
				}
			}
		})
	}
}

// TestMultiReplica_LeaveMidFlight — close a Bus mid-stream; the
// remaining replicas must keep delivering.
func TestMultiReplica_LeaveMidFlight(t *testing.T) {
	def := events.EventDef{
		Name:        "chat.message",
		Description: "leave-mid-flight",
		Delivery:    []string{"push"},
	}
	c := newCluster(t, 3, def)
	c.replicas[0].startSubscriber(t, "")
	c.replicas[1].startSubscriber(t, "")

	// First yield reaches both subscribers.
	require.NoError(t, c.replicas[2].yield(context.Background(), payloadFor("", "first")))
	c.waitForFrameCount(t, c.replicas[0], 1, time.Second)
	c.waitForFrameCount(t, c.replicas[1], 1, time.Second)

	// Close replica 0's bus. Subsequent yields should still reach
	// replica 1.
	require.NoError(t, c.replicas[0].bus.Close())
	require.NoError(t, c.replicas[2].yield(context.Background(), payloadFor("", "second")))
	c.waitForFrameCount(t, c.replicas[1], 2, time.Second)

	// Replica 0 may or may not have received second depending on
	// timing of Close vs publish; the contract is "after Close
	// completes no more events flow." We don't assert on replica
	// 0's final count beyond ≥ 1.
	assert.GreaterOrEqual(t, len(c.replicas[0].snapshot()), 1)
}

// TestMultiReplica_HighConcurrency — race detector workout: many
// concurrent yields across many replicas with many subscribers.
// Verifies the Hub + Bus don't race on the shared maps + queues.
func TestMultiReplica_HighConcurrency(t *testing.T) {
	def := events.EventDef{
		Name:        "chat.message",
		Description: "high-concurrency",
		Delivery:    []string{"push"},
	}
	c := newCluster(t, 4, def)
	for _, r := range c.replicas {
		r.startSubscriber(t, "")
	}

	const yieldsPerReplica = 16
	var wg sync.WaitGroup
	var yielded atomic.Int32
	for _, r := range c.replicas {
		wg.Add(1)
		go func(rr *replica) {
			defer wg.Done()
			for i := 0; i < yieldsPerReplica; i++ {
				_ = rr.yield(context.Background(), payloadFor("", fmt.Sprintf("r%d-y%d", rr.idx, i)))
				yielded.Add(1)
			}
		}(r)
	}
	wg.Wait()

	// Every replica's slot should see every yielded event
	// (4 replicas × 16 yields = 64 total).
	expected := len(c.replicas) * yieldsPerReplica
	for _, r := range c.replicas {
		c.waitForFrameCount(t, r, expected, 2*time.Second)
		assert.Len(t, r.snapshot(), expected, "replica %d", r.idx)
	}
}
