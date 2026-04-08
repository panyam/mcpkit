package server

import (
	"sync"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// TestBroadcastSingleSession verifies that a single connected session receives
// a broadcast notification with the correct method name and params. This is the
// basic contract test for Server.Broadcast — one session, one notification, exact
// method and params match.
func TestBroadcastSingleSession(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	// Create a session and capture notifications
	d := srv.newSession()
	d.sessionID = "test-1"

	var mu sync.Mutex
	var captured []struct {
		method string
		params any
	}
	d.notifyFunc = func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, struct {
			method string
			params any
		}{method, params})
	}

	// Register a broadcaster that iterates our test session
	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(method string, params any) {
			if d.notifyFunc != nil {
				d.notifyFunc(method, params)
			}
		},
	)

	srv.Broadcast("notifications/tools/list_changed", nil)

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("got %d notifications, want 1", len(captured))
	}
	if captured[0].method != "notifications/tools/list_changed" {
		t.Errorf("method = %q, want notifications/tools/list_changed", captured[0].method)
	}
	if captured[0].params != nil {
		t.Errorf("params = %v, want nil", captured[0].params)
	}
}

// TestBroadcastMultipleSessions verifies that when multiple sessions are
// connected (potentially across different transports), ALL of them receive
// exactly one broadcast notification each. This confirms the fan-out logic
// iterates every registered broadcaster closure.
func TestBroadcastMultipleSessions(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	var mu sync.Mutex
	counts := map[string]int{}

	// Simulate two transports, each with one session
	for _, id := range []string{"session-1", "session-2"} {
		id := id
		d := srv.newSession()
		d.sessionID = id
		d.notifyFunc = func(method string, params any) {
			mu.Lock()
			defer mu.Unlock()
			counts[id]++
		}

		srv.registerTransportSessions(
			func(sid string) bool { return false },
			func() {},
			func(method string, params any) {
				if d.notifyFunc != nil {
					d.notifyFunc(method, params)
				}
			},
		)
	}

	srv.Broadcast("notifications/prompts/list_changed", nil)

	mu.Lock()
	defer mu.Unlock()
	if counts["session-1"] != 1 {
		t.Errorf("session-1 got %d notifications, want 1", counts["session-1"])
	}
	if counts["session-2"] != 1 {
		t.Errorf("session-2 got %d notifications, want 1", counts["session-2"])
	}
}

// TestBroadcastSkipsNilNotifyFunc verifies that sessions where notifyFunc is nil
// (e.g., a Streamable HTTP session without a GET SSE stream) are safely skipped
// during broadcast — no panic, no error, other sessions still receive the
// notification.
func TestBroadcastSkipsNilNotifyFunc(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	var received bool

	// Session with nil notifyFunc (e.g., Streamable HTTP without GET SSE)
	dNil := srv.newSession()
	dNil.sessionID = "nil-session"
	// dNil.notifyFunc intentionally left nil

	// Session with working notifyFunc
	dOk := srv.newSession()
	dOk.sessionID = "ok-session"
	dOk.notifyFunc = func(method string, params any) {
		received = true
	}

	// Register a broadcaster that iterates both sessions
	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(method string, params any) {
			for _, d := range []*Dispatcher{dNil, dOk} {
				if d.notifyFunc != nil {
					d.notifyFunc(method, params)
				}
			}
		},
	)

	// Must not panic
	srv.Broadcast("notifications/tools/list_changed", nil)

	if !received {
		t.Error("ok-session did not receive notification")
	}
}

// TestBroadcastNoSessions verifies that calling Broadcast when no sessions
// are connected is a safe no-op — no panic, no error. This covers the empty
// server case (startup before any clients connect).
func TestBroadcastNoSessions(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	// No sessions registered — must not panic
	srv.Broadcast("notifications/tools/list_changed", nil)
}

// TestBroadcastDoesNotRequireSubscription verifies the key difference between
// Broadcast and NotifyResourceUpdated: Broadcast delivers to ALL connected
// sessions unconditionally, without requiring them to call resources/subscribe.
// This is the primary motivation for issue #146.
func TestBroadcastDoesNotRequireSubscription(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"}, WithSubscriptions())

	d := srv.newSession()
	d.sessionID = "no-sub"
	initDispatcher(d)

	var received bool
	d.notifyFunc = func(method string, params any) {
		received = true
	}

	// Register broadcaster
	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(method string, params any) {
			if d.notifyFunc != nil {
				d.notifyFunc(method, params)
			}
		},
	)

	// Do NOT subscribe — Broadcast should still deliver
	srv.Broadcast("notifications/resources/list_changed", nil)

	if !received {
		t.Error("session did not receive broadcast despite not subscribing")
	}
}

// TestBroadcastWithParams verifies that structured params are passed through
// to all sessions correctly, preserving the exact object. This confirms the
// params aren't dropped or mangled during the broadcast fan-out.
func TestBroadcastWithParams(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	d := srv.newSession()
	d.sessionID = "params-test"

	var capturedParams any
	d.notifyFunc = func(method string, params any) {
		capturedParams = params
	}

	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(method string, params any) {
			if d.notifyFunc != nil {
				d.notifyFunc(method, params)
			}
		},
	)

	payload := map[string]any{"uri": "test://doc", "reason": "updated"}
	srv.Broadcast("notifications/resources/updated", payload)

	m, ok := capturedParams.(map[string]any)
	if !ok {
		t.Fatalf("params type = %T, want map[string]any", capturedParams)
	}
	if m["uri"] != "test://doc" {
		t.Errorf("params[uri] = %v, want test://doc", m["uri"])
	}
	if m["reason"] != "updated" {
		t.Errorf("params[reason] = %v, want updated", m["reason"])
	}
}
