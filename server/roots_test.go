package server_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRootsNotificationHandled verifies that the server handles
// notifications/roots/list_changed without error. This notification
// is sent by clients when their available filesystem roots change.
// Uses Server.Dispatch directly (no transport, no fetch) — the fetch
// path is covered by TestRootsListFetchedAfterListChanged and friends.
func TestRootsNotificationHandled(t *testing.T) {
	srv := testutil.NewTestServer()
	testutil.InitHandshake(srv)

	// Send the notification — should not error or panic
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/roots/list_changed",
	})
	assert.Nil(t, resp, "notification should not return a response")
}

// TestRootsNotificationBeforeInit verifies that a roots notification sent
// before initialization is handled gracefully: the capability gate in
// handleRootsListChanged observes nil ClientCapabilities and returns early
// without panicking, mutating state, or issuing a fetch.
func TestRootsNotificationBeforeInit(t *testing.T) {
	srv := testutil.NewTestServer()

	// Send notification before init — should not panic
	resp := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/roots/list_changed",
	})
	assert.Nil(t, resp)
}

// TestRootType verifies that Root struct serializes correctly to JSON
// matching the MCP spec format.
func TestRootType(t *testing.T) {
	root := core.Root{
		URI:  "file:///home/user/project",
		Name: "My Project",
	}

	data, err := json.Marshal(root)
	assert.NoError(t, err)
	assert.Contains(t, string(data), `"uri":"file:///home/user/project"`)
	assert.Contains(t, string(data), `"name":"My Project"`)
}

// --- Roots-fetch test harness ---

// rootsHarness wires an InProcessTransport to a server so the notification
// handler can issue a real roots/list request back to the harness. The harness
// records each invocation of the server's WithOnRootsChanged callback plus
// each outbound roots/list request for assertion.
//
// Design choice: tests exercise the real dispatch + transport + pending-map
// path rather than stubbing the dispatcher, because the whole point of #26
// is the *plumbing* between notification reception and server-initiated
// request dispatch. Stubbing would route around the bug.
type rootsHarness struct {
	t        *testing.T
	srv      *server.Server
	xport    *server.InProcessTransport
	calls    []rootsCallbackInvocation
	mu       sync.Mutex
	reqCount atomic.Int32

	// rootsResponder is invoked on every incoming roots/list server request.
	// Tests set this to control the response (static list, error, etc.).
	rootsResponder func(req *core.Request) *core.Response
}

type rootsCallbackInvocation struct {
	roots []core.Root
	at    time.Time
}

// newRootsHarness constructs a server + in-process transport wired for
// roots-list fetch testing. clientRootsCap controls whether the simulated
// client declares `capabilities.roots.listChanged` in the initialize
// handshake — TestRootsNotFetchedWithoutCapability sets this false.
func newRootsHarness(t *testing.T, clientRootsCap bool) *rootsHarness {
	t.Helper()
	h := &rootsHarness{t: t}

	h.srv = server.NewServer(
		core.ServerInfo{Name: "roots-test", Version: "1.0.0"},
		server.WithOnRootsChanged(func(roots []core.Root) {
			// Defensive-copy so tests observing the captured slice aren't
			// fooled by later mutations from the harness or dispatcher.
			copied := append([]core.Root(nil), roots...)
			h.mu.Lock()
			h.calls = append(h.calls, rootsCallbackInvocation{roots: copied, at: time.Now()})
			h.mu.Unlock()
		}),
	)
	// A no-op tool so InitHandshake-style initialize requests succeed.
	h.srv.RegisterTool(
		core.ToolDef{Name: "noop", Description: "noop", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)

	h.xport = server.NewInProcessTransport(h.srv,
		server.WithServerRequestHandler(func(ctx context.Context, req *core.Request) *core.Response {
			if req.Method == "roots/list" {
				h.reqCount.Add(1)
				if h.rootsResponder != nil {
					return h.rootsResponder(req)
				}
				return core.NewResponse(req.ID, core.RootsListResult{Roots: nil})
			}
			return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound, "unsupported: "+req.Method)
		}),
	)
	require.NoError(t, h.xport.Connect(context.Background()))

	// Build the initialize params, optionally declaring roots.listChanged.
	caps := map[string]any{}
	if clientRootsCap {
		caps["roots"] = map[string]any{"listChanged": true}
	}
	paramsRaw, err := json.Marshal(map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    caps,
		"clientInfo":      map[string]any{"name": "roots-test-client", "version": "1.0"},
	})
	require.NoError(t, err)

	// Run the initialize + notifications/initialized handshake through the
	// transport so the session dispatcher (not the top-level one) gets the
	// client capabilities.
	initResp, err := h.xport.Call(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`0`),
		Method:  "initialize",
		Params:  paramsRaw,
	})
	require.NoError(t, err)
	require.NotNil(t, initResp)
	require.Nil(t, initResp.Error)

	_, err = h.xport.Call(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	require.NoError(t, err)

	return h
}

// fireListChanged sends a single notifications/roots/list_changed via the
// transport. Notifications return no response; any reply is dropped.
func (h *rootsHarness) fireListChanged() {
	h.t.Helper()
	_, err := h.xport.Call(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/roots/list_changed",
	})
	require.NoError(h.t, err)
}

// waitForCallbacks polls until the callback has fired at least want times or
// the deadline elapses. Returns the number of observed callbacks.
func (h *rootsHarness) waitForCallbacks(want int, within time.Duration) int {
	h.t.Helper()
	deadline := time.Now().Add(within)
	for {
		h.mu.Lock()
		n := len(h.calls)
		h.mu.Unlock()
		if n >= want {
			return n
		}
		if time.Now().After(deadline) {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// callbackInvocations returns a snapshot of all observed callback invocations.
func (h *rootsHarness) callbackInvocations() []rootsCallbackInvocation {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]rootsCallbackInvocation(nil), h.calls...)
}

// --- Roots fetch tests ---

// TestRootsListFetchedAfterListChanged verifies the full roots refresh cycle:
// the server receives notifications/roots/list_changed, issues a
// server-to-client roots/list request, stores the populated list, and invokes
// the WithOnRootsChanged callback with the populated list (not nil).
//
// Red-state before #26: callback fires immediately with d.roots (always nil),
// no roots/list request is ever issued, reqCount stays 0.
func TestRootsListFetchedAfterListChanged(t *testing.T) {
	h := newRootsHarness(t, true)
	expected := []core.Root{
		{URI: "file:///home/alice/proj", Name: "alice-proj"},
		{URI: "file:///home/alice/docs"},
	}
	h.rootsResponder = func(req *core.Request) *core.Response {
		return core.NewResponse(req.ID, core.RootsListResult{Roots: expected})
	}

	h.fireListChanged()

	got := h.waitForCallbacks(1, 2*time.Second)
	require.Equal(t, 1, got, "callback should fire exactly once after fetch completes")

	invs := h.callbackInvocations()
	require.Len(t, invs, 1)
	assert.Equal(t, expected, invs[0].roots,
		"callback must receive the fetched roots list, not nil")

	assert.Equal(t, int32(1), h.reqCount.Load(),
		"server should issue exactly one roots/list request")
}

// TestRootsNotFetchedWithoutCapability verifies the capability gate:
// clients that do NOT declare capabilities.roots.listChanged do not receive
// a roots/list request, and the callback does not fire. Per MCP spec, only
// clients that support the capability can respond to roots/list.
func TestRootsNotFetchedWithoutCapability(t *testing.T) {
	h := newRootsHarness(t, false)
	h.rootsResponder = func(req *core.Request) *core.Response {
		t.Errorf("roots/list should NOT be sent to a client without roots.listChanged")
		return core.NewResponse(req.ID, core.RootsListResult{})
	}

	h.fireListChanged()

	// Give any (erroneous) async fetch time to fire.
	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, int32(0), h.reqCount.Load(),
		"no roots/list request should be issued when client lacks capability")
	assert.Empty(t, h.callbackInvocations(),
		"callback must not fire when no fetch occurred")
}

// TestRootsCallbackReceivesPopulatedList is a regression guard for the
// pre-#26 latent bug where d.roots was never assigned and the callback was
// always invoked with nil. Asserts the callback argument is non-nil and
// matches the fetched list exactly.
func TestRootsCallbackReceivesPopulatedList(t *testing.T) {
	h := newRootsHarness(t, true)
	want := []core.Root{{URI: "file:///tmp/x", Name: "x"}}
	h.rootsResponder = func(req *core.Request) *core.Response {
		return core.NewResponse(req.ID, core.RootsListResult{Roots: want})
	}

	h.fireListChanged()
	require.Equal(t, 1, h.waitForCallbacks(1, 2*time.Second))

	invs := h.callbackInvocations()
	require.Len(t, invs, 1)
	require.NotNil(t, invs[0].roots, "callback must receive non-nil roots")
	assert.Equal(t, want, invs[0].roots)
}

// TestRootsFetchErrorDoesNotCrash verifies error-path resilience: when the
// client responds to roots/list with a JSON-RPC error, the server logs and
// does NOT invoke the callback with garbage data. The process must not
// crash or leak goroutines (verified by the test not hanging).
func TestRootsFetchErrorDoesNotCrash(t *testing.T) {
	h := newRootsHarness(t, true)
	h.rootsResponder = func(req *core.Request) *core.Response {
		return core.NewErrorResponse(req.ID, core.ErrCodeInternal, "simulated client failure")
	}

	h.fireListChanged()
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int32(1), h.reqCount.Load(),
		"server should issue the roots/list request even though it errors")
	assert.Empty(t, h.callbackInvocations(),
		"callback must not fire on fetch error")
}

// TestRootsFetchDedup verifies that a burst of list_changed notifications
// does NOT spawn N concurrent roots/list requests. The dedup guard bounds
// concurrent fetches to 1 per session; late notifications during an
// in-flight fetch trigger at most one coalesced re-fetch.
//
// Why <=2 and not exactly 1: the refresh goroutine may already be mid-fetch
// when the 2nd notification arrives, which legitimately triggers a second
// fetch to pick up the newly-invalidated state. Anything beyond 2 means the
// dedup guard failed.
func TestRootsFetchDedup(t *testing.T) {
	h := newRootsHarness(t, true)

	// Slow responder: simulate a client that takes ~100ms to list its roots.
	// Without dedup, all N concurrent notifications would each fire their own
	// request and wait the full 100ms in parallel.
	h.rootsResponder = func(req *core.Request) *core.Response {
		time.Sleep(100 * time.Millisecond)
		return core.NewResponse(req.ID, core.RootsListResult{
			Roots: []core.Root{{URI: "file:///dedup", Name: "dedup"}},
		})
	}

	// Fire 5 notifications back-to-back. The first spawns the fetch; the next
	// 4 should fold into at most one coalesced re-fetch.
	for i := 0; i < 5; i++ {
		h.fireListChanged()
	}

	// Allow time for all in-flight fetches plus the optional coalesced re-fetch.
	time.Sleep(200 * time.Millisecond)

	got := h.reqCount.Load()
	assert.LessOrEqual(t, got, int32(2),
		"dedup guard should bound concurrent fetches to ≤2, got %d", got)
	assert.GreaterOrEqual(t, got, int32(1),
		"at least one fetch must be issued")
}
