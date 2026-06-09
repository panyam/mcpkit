package server

import (
	"context"
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
	d.SetNotifyFunc(func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, struct {
			method string
			params any
		}{method, params})
	})

	// Register a broadcaster that iterates our test session
	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(_ context.Context, method string, params any) {
			if fn := d.getNotifyFunc(); fn != nil {
				fn(method, params)
			}
		},
	)

	srv.Broadcast(context.Background(), "notifications/tools/list_changed", nil)

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
		d.SetNotifyFunc(func(method string, params any) {
			mu.Lock()
			defer mu.Unlock()
			counts[id]++
		})

		srv.registerTransportSessions(
			func(sid string) bool { return false },
			func() {},
			func(_ context.Context, method string, params any) {
				if fn := d.getNotifyFunc(); fn != nil {
					fn(method, params)
				}
			},
		)
	}

	srv.Broadcast(context.Background(), "notifications/prompts/list_changed", nil)

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
	dOk.SetNotifyFunc(func(method string, params any) {
		received = true
	})

	// Register a broadcaster that iterates both sessions
	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(_ context.Context, method string, params any) {
			for _, d := range []*Dispatcher{dNil, dOk} {
				if fn := d.getNotifyFunc(); fn != nil {
					fn(method, params)
				}
			}
		},
	)

	// Must not panic
	srv.Broadcast(context.Background(), "notifications/tools/list_changed", nil)

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
	srv.Broadcast(context.Background(), "notifications/tools/list_changed", nil)
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
	d.SetNotifyFunc(func(method string, params any) {
		received = true
	})

	// Register broadcaster
	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(_ context.Context, method string, params any) {
			if fn := d.getNotifyFunc(); fn != nil {
				fn(method, params)
			}
		},
	)

	// Do NOT subscribe — Broadcast should still deliver
	srv.Broadcast(context.Background(), "notifications/resources/list_changed", nil)

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
	d.SetNotifyFunc(func(method string, params any) {
		capturedParams = params
	})

	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(_ context.Context, method string, params any) {
			if fn := d.getNotifyFunc(); fn != nil {
				fn(method, params)
			}
		},
	)

	payload := map[string]any{"uri": "test://doc", "reason": "updated"}
	srv.Broadcast(context.Background(), "notifications/resources/updated", payload)

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

// --- SEP-414 trace context injection on Broadcast (issue 715) ---

const (
	testBroadcastTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	testBroadcastTracestate  = "vendor=val"
)

// captureBroadcastParams wires a single test session whose notifyFunc
// records the params handed in. Returns the dispatcher (for stale
// references in the caller) and a pointer to the captured params.
func captureBroadcastParams(t *testing.T, srv *Server) (*Dispatcher, *any) {
	t.Helper()
	d := srv.newSession()
	d.sessionID = "trace-test"
	var captured any
	d.SetNotifyFunc(func(_ string, params any) {
		captured = params
	})
	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(_ context.Context, method string, params any) {
			if fn := d.getNotifyFunc(); fn != nil {
				fn(method, params)
			}
		},
	)
	return d, &captured
}

// TestBroadcast_InjectsTraceContextFromCtx is the core acceptance for
// issue 715: when Broadcast is called with ctx carrying a non-zero
// core.TraceContext (the shape `events.Emit` threads through after PR
// 712/714), the SSE-pushed notification params carry `_meta.traceparent`
// so downstream subscribers' spans stitch into the originating trace.
func TestBroadcast_InjectsTraceContextFromCtx(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	_, captured := captureBroadcastParams(t, srv)

	tc := core.TraceContext{
		Traceparent: testBroadcastTraceparent,
		Tracestate:  testBroadcastTracestate,
	}
	ctx := core.WithTraceContext(context.Background(), tc)
	srv.Broadcast(ctx, "notifications/events/event", map[string]any{"name": "demo"})

	m, ok := (*captured).(map[string]any)
	if !ok {
		t.Fatalf("params type = %T, want map[string]any", *captured)
	}
	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %v, want map[string]any (Broadcast must inject when ctx carries a non-zero TraceContext)", m["_meta"])
	}
	if got := meta[core.MetaKeyTraceparent]; got != testBroadcastTraceparent {
		t.Errorf("_meta.traceparent = %v, want %q", got, testBroadcastTraceparent)
	}
	if got := meta[core.MetaKeyTracestate]; got != testBroadcastTracestate {
		t.Errorf("_meta.tracestate = %v, want %q", got, testBroadcastTracestate)
	}
}

// TestBroadcast_NoTraceContext_LeavesParamsUntouched pins the
// zero-overhead path: a plain context.Background() (no trace context
// attached) MUST NOT mutate params or synthesize an empty _meta. This
// is the regression guard against accidental empty-_meta injection
// that would change every broadcast's wire shape even when tracing
// isn't wired.
func TestBroadcast_NoTraceContext_LeavesParamsUntouched(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	_, captured := captureBroadcastParams(t, srv)

	original := map[string]any{"uri": "test://doc"}
	srv.Broadcast(context.Background(), "notifications/resources/updated", original)

	m, ok := (*captured).(map[string]any)
	if !ok {
		t.Fatalf("params type = %T, want map[string]any (untouched pass-through)", *captured)
	}
	if _, present := m["_meta"]; present {
		t.Errorf("_meta MUST NOT be created when ctx carries no TraceContext; got %v", m["_meta"])
	}
	if m["uri"] != "test://doc" {
		t.Errorf("original keys MUST survive untouched; got %v", m)
	}
}

// TestBroadcast_CallerSetMetaTraceparent_Wins pins that an explicit
// caller-set `_meta.traceparent` on params is preserved when ctx also
// carries a TraceContext — delegates to core.InjectTraceContextIntoParams's
// caller-set-wins contract, which the events bus relay (PR 712) also
// relies on.
func TestBroadcast_CallerSetMetaTraceparent_Wins(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	_, captured := captureBroadcastParams(t, srv)

	callerTraceparent := "00-11111111111111111111111111111111-2222222222222222-01"
	params := map[string]any{
		"name": "demo",
		"_meta": map[string]any{
			core.MetaKeyTraceparent: callerTraceparent,
		},
	}
	tc := core.TraceContext{Traceparent: testBroadcastTraceparent}
	ctx := core.WithTraceContext(context.Background(), tc)
	srv.Broadcast(ctx, "notifications/events/event", params)

	m := (*captured).(map[string]any)
	meta := m["_meta"].(map[string]any)
	if got := meta[core.MetaKeyTraceparent]; got != callerTraceparent {
		t.Errorf("_meta.traceparent = %v, want caller-set %q (ctx-derived must not clobber)", got, callerTraceparent)
	}
}

// TestBroadcast_NonObjectParams_LeavesUntouched pins the JSON-RPC
// non-object params escape hatch: positional arrays / scalars carry no
// `_meta` envelope per the JSON-RPC spec, so the injection helper
// returns the params unchanged. Broadcast surfaces the same behavior.
func TestBroadcast_NonObjectParams_LeavesUntouched(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	_, captured := captureBroadcastParams(t, srv)

	positional := []any{"arg1", 42}
	tc := core.TraceContext{Traceparent: testBroadcastTraceparent}
	ctx := core.WithTraceContext(context.Background(), tc)
	srv.Broadcast(ctx, "notifications/events/event", positional)

	got, ok := (*captured).([]any)
	if !ok {
		t.Fatalf("params type = %T, want []any (non-object MUST pass through untouched)", *captured)
	}
	if len(got) != 2 || got[0] != "arg1" || got[1] != 42 {
		t.Errorf("non-object params mutated: got %v", got)
	}
}
