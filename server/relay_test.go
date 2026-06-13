package server

import (
	"context"
	"sync"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- CapabilityBroadcastReceiver -----------------------------------

type captureBroadcastSession struct {
	mu     sync.Mutex
	frames []captureFrame
}

type captureFrame struct {
	method string
	params any
}

func (c *captureBroadcastSession) frame() []captureFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]captureFrame, len(c.frames))
	copy(out, c.frames)
	return out
}

func newServerWithCapture(t *testing.T) (*Server, *captureBroadcastSession) {
	t.Helper()
	srv := NewServer(core.ServerInfo{Name: "relay-test", Version: "0.0.1"})
	cap := &captureBroadcastSession{}
	srv.registerTransportSessions(
		func(string) bool { return false },
		func() {},
		func(_ context.Context, method string, params any) {
			cap.mu.Lock()
			cap.frames = append(cap.frames, captureFrame{method: method, params: params})
			cap.mu.Unlock()
		},
	)
	return srv, cap
}

func TestCapabilityBroadcastReceiver_ForwardsToBroadcast(t *testing.T) {
	srv, cap := newServerWithCapture(t)

	recv := NewCapabilityBroadcastReceiver(srv)
	recv.Receive(context.Background(), "notifications/tools/list_changed", nil)

	frames := cap.frame()
	require.Len(t, frames, 1)
	assert.Equal(t, "notifications/tools/list_changed", frames[0].method)
}

func TestCapabilityBroadcastReceiver_ForwardsParams(t *testing.T) {
	srv, cap := newServerWithCapture(t)

	recv := NewCapabilityBroadcastReceiver(srv)
	params := map[string]any{"uri": "file:///foo"}
	recv.Receive(context.Background(), "notifications/resources/updated", params)

	frames := cap.frame()
	require.Len(t, frames, 1)
	got, _ := frames[0].params.(map[string]any)
	assert.Equal(t, "file:///foo", got["uri"])
}

func TestCapabilityBroadcastReceiver_ConcurrentReceivesAreSafe(t *testing.T) {
	srv, cap := newServerWithCapture(t)
	recv := NewCapabilityBroadcastReceiver(srv)

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			recv.Receive(context.Background(), "notifications/tools/list_changed", nil)
		}()
	}
	wg.Wait()

	assert.Len(t, cap.frame(), n)
}

// TestNotificationRelayReceiver_Conformance is a compile-time check
// that the reference implementations satisfy the interface — fails
// to build if the interface drifts.
func TestNotificationRelayReceiver_Conformance(t *testing.T) {
	var _ NotificationRelayReceiver = (*CapabilityBroadcastReceiver)(nil)
	var _ NotificationRelayReceiver = (*NotificationRouter)(nil)
	var _ NotificationRelayReceiver = (*ResourcesUpdatedReceiver)(nil)
}

// --- ResourcesUpdatedReceiver --------------------------------------

func TestResourcesUpdatedReceiver_RoutesToLocalSubscribers(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "rur", Version: "0.0.1"}, WithSubscriptions())

	// Subscribe a session to a URI by directly wiring the registry
	// (the public API requires an initialized session; for unit-level
	// tests we exercise the registry directly).
	d := srv.newSession()
	d.sessionID = "sess-A"
	var fired []string
	var mu sync.Mutex
	d.SetNotifyFunc(func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		// Capture only the URI-bearing notifications we care about.
		if method == "notifications/resources/updated" {
			if n, ok := params.(core.ResourceUpdatedNotification); ok {
				fired = append(fired, n.URI)
			}
		}
	})
	require.NoError(t, srv.subRegistry.subscribe("sess-A", d, "file:///foo"))

	recv := NewResourcesUpdatedReceiver(srv)
	recv.Receive(context.Background(),
		"notifications/resources/updated",
		core.ResourceUpdatedNotification{URI: "file:///foo"},
	)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, fired, 1)
	assert.Equal(t, "file:///foo", fired[0])
}

func TestResourcesUpdatedReceiver_AcceptsMapShape(t *testing.T) {
	// Receivers using a generic-decode transport (e.g. redisstore.CapabilityBus
	// decodes envelope.params as json.RawMessage which downstream may
	// unmarshal into map[string]any) should still route correctly.
	srv := NewServer(core.ServerInfo{Name: "rur-map", Version: "0.0.1"}, WithSubscriptions())

	d := srv.newSession()
	d.sessionID = "sess-A"
	var fired int
	var mu sync.Mutex
	d.SetNotifyFunc(func(method string, _ any) {
		mu.Lock()
		defer mu.Unlock()
		if method == "notifications/resources/updated" {
			fired++
		}
	})
	require.NoError(t, srv.subRegistry.subscribe("sess-A", d, "file:///bar"))

	recv := NewResourcesUpdatedReceiver(srv)
	recv.Receive(context.Background(),
		"notifications/resources/updated",
		map[string]any{"uri": "file:///bar"},
	)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, fired)
}

func TestResourcesUpdatedReceiver_DropsWrongMethod(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "rur-wrong", Version: "0.0.1"}, WithSubscriptions())
	recv := NewResourcesUpdatedReceiver(srv)

	// Should not panic, should not crash.
	recv.Receive(context.Background(), "notifications/tools/list_changed", nil)
}

func TestResourcesUpdatedReceiver_DropsMissingURI(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "rur-empty", Version: "0.0.1"}, WithSubscriptions())
	recv := NewResourcesUpdatedReceiver(srv)

	// Params shape doesn't carry a URI — must not fire any subscribers.
	recv.Receive(context.Background(),
		"notifications/resources/updated",
		map[string]any{"unrelated": "value"},
	)
	recv.Receive(context.Background(),
		"notifications/resources/updated",
		core.ResourceUpdatedNotification{URI: ""},
	)
}

// TestSubscriptionRegistry_NotifyPublishesViaRelay verifies that
// notify() fires BOTH the local subscribers AND the installed
// NotificationRelay. notifyLocal() (the receiver's entry point) must NOT
// re-publish — that would loop through the transport.
func TestSubscriptionRegistry_NotifyPublishesViaRelay(t *testing.T) {
	relay := &recordingHandler{} // NotificationRelay = NotificationRelayReceiver with same shape
	srv := NewServer(core.ServerInfo{Name: "sub-relay", Version: "0.0.1"},
		WithSubscriptions(),
		WithNotificationRelay(broadcastRelayFromHandler{recordingHandler: relay}),
	)

	d := srv.newSession()
	d.sessionID = "sess-A"
	var localFired int
	var mu sync.Mutex
	d.SetNotifyFunc(func(method string, _ any) {
		mu.Lock()
		defer mu.Unlock()
		if method == "notifications/resources/updated" {
			localFired++
		}
	})
	require.NoError(t, srv.subRegistry.subscribe("sess-A", d, "file:///x"))

	// notify(): local + publish via relay.
	srv.NotifyResourceUpdated("file:///x")

	mu.Lock()
	assert.Equal(t, 1, localFired, "local subscriber must fire")
	mu.Unlock()
	assert.Len(t, relay.snapshot(), 1, "relay must be published to")
	assert.Equal(t, "notifications/resources/updated", relay.snapshot()[0].method)
}

// TestSubscriptionRegistry_NotifyLocalSkipsRelay verifies that
// notifyLocal() (used by ResourcesUpdatedReceiver on the receive side)
// does NOT republish — otherwise cross-replica notifications would
// loop.
func TestSubscriptionRegistry_NotifyLocalSkipsRelay(t *testing.T) {
	relay := &recordingHandler{}
	srv := NewServer(core.ServerInfo{Name: "sub-local", Version: "0.0.1"},
		WithSubscriptions(),
		WithNotificationRelay(broadcastRelayFromHandler{recordingHandler: relay}),
	)

	d := srv.newSession()
	d.sessionID = "sess-A"
	var localFired int
	var mu sync.Mutex
	d.SetNotifyFunc(func(method string, _ any) {
		mu.Lock()
		defer mu.Unlock()
		if method == "notifications/resources/updated" {
			localFired++
		}
	})
	require.NoError(t, srv.subRegistry.subscribe("sess-A", d, "file:///x"))

	srv.NotifyResourceUpdatedLocal("file:///x")

	mu.Lock()
	assert.Equal(t, 1, localFired)
	mu.Unlock()
	assert.Empty(t, relay.snapshot(), "notifyLocal must NOT publish through the relay")
}

// broadcastRelayFromHandler adapts recordingHandler (a
// NotificationRelayReceiver) into a NotificationRelay so tests can install
// it via WithNotificationRelay and assert on publish frames.
type broadcastRelayFromHandler struct {
	*recordingHandler
}

func (b broadcastRelayFromHandler) Publish(ctx context.Context, method string, params any) {
	b.recordingHandler.Receive(ctx, method, params)
}

// --- NotificationRouter ----------------------------------------

type recordingHandler struct {
	mu     sync.Mutex
	frames []captureFrame
}

func (r *recordingHandler) Receive(_ context.Context, method string, params any) {
	r.mu.Lock()
	r.frames = append(r.frames, captureFrame{method: method, params: params})
	r.mu.Unlock()
}

func (r *recordingHandler) snapshot() []captureFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]captureFrame, len(r.frames))
	copy(out, r.frames)
	return out
}

func TestNotificationRouter_RoutesByMethod(t *testing.T) {
	mux := NewNotificationRouter()
	tools := &recordingHandler{}
	resources := &recordingHandler{}
	mux.Handle("notifications/tools/list_changed", tools)
	mux.Handle("notifications/resources/list_changed", resources)

	mux.Receive(context.Background(), "notifications/tools/list_changed", nil)
	mux.Receive(context.Background(), "notifications/resources/list_changed", nil)
	mux.Receive(context.Background(), "notifications/resources/list_changed", nil)

	assert.Len(t, tools.snapshot(), 1)
	assert.Len(t, resources.snapshot(), 2)
}

func TestNotificationRouter_UnknownMethodDropped(t *testing.T) {
	mux := NewNotificationRouter()
	tools := &recordingHandler{}
	mux.Handle("notifications/tools/list_changed", tools)

	mux.Receive(context.Background(), "notifications/some/unknown", nil)

	assert.Empty(t, tools.snapshot(), "unknown methods must not fall through to any handler")
}

func TestNotificationRouter_HandleReplacesExisting(t *testing.T) {
	mux := NewNotificationRouter()
	first := &recordingHandler{}
	second := &recordingHandler{}
	mux.Handle("notifications/tools/list_changed", first)
	mux.Handle("notifications/tools/list_changed", second)

	mux.Receive(context.Background(), "notifications/tools/list_changed", nil)

	assert.Empty(t, first.snapshot(), "first handler must be replaced")
	assert.Len(t, second.snapshot(), 1)
}

func TestNotificationRouter_HandleNilIsNoOp(t *testing.T) {
	mux := NewNotificationRouter()
	mux.Handle("notifications/tools/list_changed", nil)
	mux.Receive(context.Background(), "notifications/tools/list_changed", nil)
	// No panic, no handler fired — nothing to assert beyond "didn't crash".
}

func TestNotificationRouter_ConcurrentRegisterAndDispatch(t *testing.T) {
	mux := NewNotificationRouter()
	handler := &recordingHandler{}
	mux.Handle("notifications/tools/list_changed", handler)

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n * 2)
	// n goroutines registering (overwriting), n dispatching.
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			mux.Handle("notifications/tools/list_changed", handler)
		}()
		go func() {
			defer wg.Done()
			mux.Receive(context.Background(), "notifications/tools/list_changed", nil)
		}()
	}
	wg.Wait()

	assert.Len(t, handler.snapshot(), n, "every dispatch should reach the handler")
}
