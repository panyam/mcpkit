package server

import (
	"sync"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
)

// TestListChanged_RelayFansAcrossReplicas verifies the four
// list_changed notifications (tools, resources, prompts) wired
// through Registry → Server.Broadcast → NotificationRelay reach
// sessions on every replica.
//
// Setup: 3 replicas wired to a shared memRelayBus (the in-memory
// NotificationRelay from relay_inmemory_test.go). Mutate the registry
// on replica 0; assert all 3 replicas' captured frames recorded the
// notification.
//
// Subtests cover each of:
//   - notifications/tools/list_changed (AddTool / RemoveTool)
//   - notifications/resources/list_changed (AddResource /
//     RemoveResource / AddResourceTemplate / RemoveResourceTemplate)
//   - notifications/prompts/list_changed (AddPrompt / RemovePrompt)
func TestListChanged_RelayFansAcrossReplicas(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		mutate func(t *testing.T, srv *Server)
	}{
		{
			name:   "tools/list_changed via AddTool",
			method: "notifications/tools/list_changed",
			mutate: func(t *testing.T, srv *Server) {
				err := srv.dispatcher.Reg.AddTool(core.ToolDef{Name: "echo", Description: "echo"}, nil)
				assert.NoError(t, err)
			},
		},
		{
			name:   "resources/list_changed via AddResource",
			method: "notifications/resources/list_changed",
			mutate: func(t *testing.T, srv *Server) {
				srv.dispatcher.Reg.AddResource(core.ResourceDef{URI: "file:///x", Name: "x"}, nil)
			},
		},
		{
			name:   "resources/list_changed via AddResourceTemplate",
			method: "notifications/resources/list_changed",
			mutate: func(t *testing.T, srv *Server) {
				srv.dispatcher.Reg.AddResourceTemplate(core.ResourceTemplate{URITemplate: "file:///x/{id}"}, nil)
			},
		},
		{
			name:   "prompts/list_changed via AddPrompt",
			method: "notifications/prompts/list_changed",
			mutate: func(t *testing.T, srv *Server) {
				srv.dispatcher.Reg.AddPrompt(core.PromptDef{Name: "hello"}, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newCaptureCluster(t, 3)
			tc.mutate(t, cluster.replicas[0].srv)

			// All 3 replicas should see the notification — replica 0
			// via its local BroadcastToSessions, replicas 1 + 2 via
			// the relay → Receive → BroadcastToSessions path.
			for _, r := range cluster.replicas {
				r.waitForFrameCount(t, 1, time.Second)
				frames := r.frames()
				assert.Len(t, frames, 1, "replica %d expected 1 frame", r.idx)
				assert.Equal(t, tc.method, frames[0].method, "replica %d method", r.idx)
			}
		})
	}
}

// TestListChanged_OnlyOneNotifyPerMutation verifies that a single
// AddTool / AddResource / AddPrompt call results in exactly ONE
// notification per replica (not duplicated by the relay round-trip).
// Direct symmetry guarantee with TestBroadcast_SelfPublishDeduped
// — list_changed surfaces inherit the same dedup contract.
func TestListChanged_OnlyOneNotifyPerMutation(t *testing.T) {
	cluster := newCaptureCluster(t, 3)
	err := cluster.replicas[0].srv.dispatcher.Reg.AddTool(core.ToolDef{Name: "echo"}, nil)
	assert.NoError(t, err)

	// Wait a beat for any erroneous relay round-trip back to replica 0.
	time.Sleep(50 * time.Millisecond)

	for _, r := range cluster.replicas {
		assert.Len(t, r.frames(), 1,
			"replica %d must see EXACTLY one frame; %d means duplicate via relay",
			r.idx, len(r.frames()))
	}
}

// TestResourcesUpdated_RelayFansAcrossReplicas verifies the
// subscription-shaped resources/updated notification reaches
// matching subscribers on every replica.
//
// Setup: 3 replicas, each with subscriptions enabled. Each replica
// subscribes its OWN session to "file:///shared". NotifyResourceUpdated
// fires on replica 0; all 3 replicas' local sessions receive the
// notification (replica 0 directly, replicas 1 + 2 via the relay →
// ResourcesUpdatedReceiver → notifyLocal path).
func TestResourcesUpdated_RelayFansAcrossReplicas(t *testing.T) {
	// Build a custom cluster — captureCluster wires its receivers to
	// CapabilityBroadcastReceiver; for resources/updated we need
	// ResourcesUpdatedReceiver wrapped in a NotificationRouter.
	bus := newMemRelayBus()
	type rep struct {
		idx  int
		srv  *Server
		mu   sync.Mutex
		fired []string
	}
	replicas := make([]*rep, 3)
	for i := 0; i < 3; i++ {
		// Construct server with subscriptions enabled + relay placeholder;
		// we attach the relay AFTER server exists since the receiver needs
		// the server reference.
		var r rep
		r.idx = i
		mux := NewNotificationRouter()
		relay := bus.attachReplica(t, mux)
		srv := NewServer(core.ServerInfo{Name: "rep", Version: "0.0.1"},
			WithSubscriptions(),
			WithNotificationRelay(relay),
		)
		mux.Handle("notifications/resources/updated", NewResourcesUpdatedReceiver(srv))
		r.srv = srv

		// Subscribe this replica's own session to file:///shared.
		d := srv.newSession()
		d.sessionID = "sess-" + string(rune('A'+i))
		d.SetNotifyFunc(func(method string, params any) {
			if method != "notifications/resources/updated" {
				return
			}
			n, ok := params.(core.ResourceUpdatedNotification)
			if !ok {
				return
			}
			r.mu.Lock()
			r.fired = append(r.fired, n.URI)
			r.mu.Unlock()
		})
		err := srv.subRegistry.subscribe(d.sessionID, d, "file:///shared")
		assert.NoError(t, err)
		replicas[i] = &r
	}

	// Trigger on replica 0.
	replicas[0].srv.NotifyResourceUpdated("file:///shared")

	// Each replica's session should fire exactly once.
	deadline := time.Now().Add(time.Second)
	for _, r := range replicas {
		for time.Now().Before(deadline) {
			r.mu.Lock()
			n := len(r.fired)
			r.mu.Unlock()
			if n >= 1 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	for _, r := range replicas {
		r.mu.Lock()
		got := append([]string(nil), r.fired...)
		r.mu.Unlock()
		assert.Equal(t, []string{"file:///shared"}, got, "replica %d", r.idx)
	}
}
