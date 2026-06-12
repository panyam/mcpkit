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
	recv.ReceiveRelay(context.Background(), "notifications/tools/list_changed", nil)

	frames := cap.frame()
	require.Len(t, frames, 1)
	assert.Equal(t, "notifications/tools/list_changed", frames[0].method)
}

func TestCapabilityBroadcastReceiver_ForwardsParams(t *testing.T) {
	srv, cap := newServerWithCapture(t)

	recv := NewCapabilityBroadcastReceiver(srv)
	params := map[string]any{"uri": "file:///foo"}
	recv.ReceiveRelay(context.Background(), "notifications/resources/updated", params)

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
			recv.ReceiveRelay(context.Background(), "notifications/tools/list_changed", nil)
		}()
	}
	wg.Wait()

	assert.Len(t, cap.frame(), n)
}

// TestNotificationRelayReceiver_Conformance is a compile-time check
// that the reference implementation satisfies the interface — fails
// to build if the interface drifts.
func TestNotificationRelayReceiver_Conformance(t *testing.T) {
	var _ NotificationRelayReceiver = (*CapabilityBroadcastReceiver)(nil)
}
