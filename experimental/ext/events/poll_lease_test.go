package events

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/require"
)

// --- Touch / OnCreate / OnExpire semantics ---

func TestPollLeaseTable_Touch_NewVsRenew(t *testing.T) {
	var creates atomic.Int32
	t1 := NewPollLeaseTable(
		WithPollLeaseTTL(50*time.Millisecond),
		WithPollLeaseSweepInterval(time.Hour), // disable background sweeps for this test
		WithPollLeaseOnCreate(func(string, string, map[string]any) { creates.Add(1) }),
	)
	defer t1.Close()

	if !t1.Touch("alice", "alert.fired", map[string]any{"sev": "high"}) {
		t.Fatalf("first Touch returned false; want true (newly created)")
	}
	if t1.Touch("alice", "alert.fired", map[string]any{"sev": "high"}) {
		t.Fatalf("second Touch returned true; want false (renewal)")
	}
	if t1.Touch("alice", "alert.fired", map[string]any{"sev": "high"}) {
		t.Fatalf("third Touch returned true; want false (renewal)")
	}
	if creates.Load() != 1 {
		t.Fatalf("OnCreate fired %d times; want 1 across renewals", creates.Load())
	}
}

func TestPollLeaseTable_DistinctParamsCreateDistinctLeases(t *testing.T) {
	var creates atomic.Int32
	tt := NewPollLeaseTable(
		WithPollLeaseSweepInterval(time.Hour),
		WithPollLeaseOnCreate(func(string, string, map[string]any) { creates.Add(1) }),
	)
	defer tt.Close()

	tt.Touch("alice", "alert.fired", map[string]any{"sev": "high"})
	tt.Touch("alice", "alert.fired", map[string]any{"sev": "low"})
	if got := creates.Load(); got != 2 {
		t.Fatalf("OnCreate fired %d times; want 2 (distinct param sets)", got)
	}
	if tt.Len() != 2 {
		t.Fatalf("Len() = %d; want 2", tt.Len())
	}
}

func TestPollLeaseTable_AnonymousPrincipalSharesLease(t *testing.T) {
	var creates atomic.Int32
	tt := NewPollLeaseTable(
		WithPollLeaseSweepInterval(time.Hour),
		WithPollLeaseOnCreate(func(string, string, map[string]any) { creates.Add(1) }),
	)
	defer tt.Close()

	if !tt.Touch("", "alert.fired", map[string]any{"sev": "high"}) {
		t.Fatalf("first anon Touch returned false; want true")
	}
	if tt.Touch("", "alert.fired", map[string]any{"sev": "high"}) {
		t.Fatalf("second anon Touch returned true; want false (shared lease per spec L707)")
	}
	if got := creates.Load(); got != 1 {
		t.Fatalf("OnCreate fired %d times; want 1 (anon callers share)", got)
	}
}

func TestPollLeaseTable_MapIterationOrderInvariant(t *testing.T) {
	// Two params maps with identical entries but built in different
	// orders MUST hash to the same lease (canonicalJSON sorts keys).
	tt := NewPollLeaseTable(WithPollLeaseSweepInterval(time.Hour))
	defer tt.Close()

	tt.Touch("alice", "alert.fired", map[string]any{"a": 1, "b": 2})
	if tt.Touch("alice", "alert.fired", map[string]any{"b": 2, "a": 1}) {
		t.Fatalf("Touch with reordered params returned true; want false (same canonical key)")
	}
	if tt.Len() != 1 {
		t.Fatalf("Len() = %d; want 1 after reordered-params renewal", tt.Len())
	}
}

func TestPollLeaseTable_ExpiryFiresOnExpire(t *testing.T) {
	var creates, expires atomic.Int32
	tt := NewPollLeaseTable(
		WithPollLeaseTTL(20*time.Millisecond),
		WithPollLeaseSweepInterval(time.Hour), // drive sweep manually
		WithPollLeaseOnCreate(func(string, string, map[string]any) { creates.Add(1) }),
		WithPollLeaseOnExpire(func(string, string, map[string]any) { expires.Add(1) }),
	)
	defer tt.Close()

	tt.Touch("alice", "alert.fired", map[string]any{"sev": "high"})
	if creates.Load() != 1 || expires.Load() != 0 {
		t.Fatalf("after Touch: creates=%d expires=%d; want 1, 0", creates.Load(), expires.Load())
	}

	// Within TTL — sweep should NOT expire.
	tt.sweepExpiredForTest()
	if expires.Load() != 0 {
		t.Fatalf("sweep within TTL fired %d expires; want 0", expires.Load())
	}

	// Past TTL — sweep should expire exactly once.
	time.Sleep(40 * time.Millisecond)
	tt.sweepExpiredForTest()
	if expires.Load() != 1 {
		t.Fatalf("sweep past TTL fired %d expires; want 1", expires.Load())
	}
	if tt.Len() != 0 {
		t.Fatalf("Len() = %d after expiry; want 0", tt.Len())
	}
	// A second sweep with no live leases is a no-op.
	tt.sweepExpiredForTest()
	if expires.Load() != 1 {
		t.Fatalf("second sweep over empty table fired more expires (now %d); want 1", expires.Load())
	}
}

func TestPollLeaseTable_RenewalPreventsExpiry(t *testing.T) {
	var expires atomic.Int32
	tt := NewPollLeaseTable(
		WithPollLeaseTTL(40*time.Millisecond),
		WithPollLeaseSweepInterval(time.Hour),
		WithPollLeaseOnExpire(func(string, string, map[string]any) { expires.Add(1) }),
	)
	defer tt.Close()

	for i := 0; i < 4; i++ {
		tt.Touch("alice", "alert.fired", nil)
		time.Sleep(15 * time.Millisecond)
		tt.sweepExpiredForTest()
	}
	if expires.Load() != 0 {
		t.Fatalf("expires fired %d times under continuous renewal; want 0", expires.Load())
	}
	if tt.Len() != 1 {
		t.Fatalf("Len() = %d under continuous renewal; want 1", tt.Len())
	}
}

func TestPollLeaseTable_HookPanicSwallowed(t *testing.T) {
	tt := NewPollLeaseTable(
		WithPollLeaseTTL(10*time.Millisecond),
		WithPollLeaseSweepInterval(time.Hour),
		WithPollLeaseOnCreate(func(string, string, map[string]any) { panic("oncreate boom") }),
		WithPollLeaseOnExpire(func(string, string, map[string]any) { panic("onexpire boom") }),
	)
	defer tt.Close()

	// Must not propagate panic.
	tt.Touch("alice", "alert.fired", nil)
	time.Sleep(20 * time.Millisecond)
	tt.sweepExpiredForTest()
}

// --- Concurrency ---

func TestPollLeaseTable_ConcurrentTouchSafe(t *testing.T) {
	var creates atomic.Int32
	tt := NewPollLeaseTable(
		WithPollLeaseSweepInterval(time.Hour),
		WithPollLeaseOnCreate(func(string, string, map[string]any) { creates.Add(1) }),
	)
	defer tt.Close()

	const goroutines, perGoroutine = 16, 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				tt.Touch("alice", "alert.fired", map[string]any{"sev": "high"})
			}
		}()
	}
	wg.Wait()

	if creates.Load() != 1 {
		t.Fatalf("OnCreate fired %d times under concurrent Touch; want exactly 1", creates.Load())
	}
	if tt.Len() != 1 {
		t.Fatalf("Len() = %d after concurrent Touch on one tuple; want 1", tt.Len())
	}
}

// --- Channel lifecycle (the no-hang claim) ---

func TestPollLeaseTable_CloseBeforeTouch_DoesNotHang(t *testing.T) {
	tt := NewPollLeaseTable(WithPollLeaseSweepInterval(time.Hour))
	done := make(chan struct{})
	go func() {
		tt.Close()
		close(done)
	}()
	select {
	case <-done:
		// good — Close returned without waiting on doneCh because
		// the sweeper never started.
	case <-time.After(2 * time.Second):
		t.Fatalf("Close() before any Touch hung — sweeper never started, but Close awaited doneCh")
	}
}

func TestPollLeaseTable_CloseAfterTouch_StopsSweeper(t *testing.T) {
	tt := NewPollLeaseTable(
		WithPollLeaseTTL(time.Hour),
		WithPollLeaseSweepInterval(5*time.Millisecond),
	)
	tt.Touch("alice", "alert.fired", nil)

	// Let the sweeper tick at least once so we know it's running.
	time.Sleep(15 * time.Millisecond)

	before := runtime.NumGoroutine()
	tt.Close()
	// Close must wait for the sweeper to exit before returning, so a
	// sample taken immediately after MUST NOT contain it.
	after := runtime.NumGoroutine()
	if after >= before {
		// runtime.NumGoroutine fluctuates with the scheduler; ride
		// 50ms then re-sample. If still >= before, fail.
		time.Sleep(50 * time.Millisecond)
		after = runtime.NumGoroutine()
		if after >= before {
			t.Fatalf("Close did not reap the sweeper goroutine: before=%d after=%d", before, after)
		}
	}
}

func TestPollLeaseTable_TouchAfterClose_IsNoop(t *testing.T) {
	var creates atomic.Int32
	tt := NewPollLeaseTable(
		WithPollLeaseSweepInterval(time.Hour),
		WithPollLeaseOnCreate(func(string, string, map[string]any) { creates.Add(1) }),
	)
	tt.Close()

	if got := tt.Touch("alice", "alert.fired", nil); got {
		t.Fatalf("Touch after Close returned true; want false (table is closed)")
	}
	if creates.Load() != 0 {
		t.Fatalf("OnCreate fired %d times after Close; want 0", creates.Load())
	}
	if tt.Len() != 0 {
		t.Fatalf("Len() = %d after Close+Touch; want 0", tt.Len())
	}
}

func TestPollLeaseTable_DoubleClose_IsNoop(t *testing.T) {
	tt := NewPollLeaseTable(
		WithPollLeaseTTL(time.Hour),
		WithPollLeaseSweepInterval(5*time.Millisecond),
	)
	tt.Touch("alice", "alert.fired", nil)
	time.Sleep(15 * time.Millisecond)

	tt.Close()
	// Second close MUST NOT block, panic on double close(stopCh), or
	// receive from a doneCh that's already been drained.
	done := make(chan struct{})
	go func() {
		tt.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("second Close() hung")
	}
}

// --- Wire helper ---

func TestPollLeaseKey_StableAcrossEqualMaps(t *testing.T) {
	a := pollLeaseKey("alice", "alert.fired", map[string]any{"a": 1, "b": 2})
	b := pollLeaseKey("alice", "alert.fired", map[string]any{"b": 2, "a": 1})
	if a != b {
		t.Errorf("pollLeaseKey not stable across map iteration order: %q vs %q", a, b)
	}
}

func TestPollLeaseKey_DistinctOnPrincipal(t *testing.T) {
	a := pollLeaseKey("alice", "alert.fired", nil)
	b := pollLeaseKey("bob", "alert.fired", nil)
	if a == b {
		t.Errorf("pollLeaseKey collided across principals: %q", a)
	}
}

func TestPollLeaseKey_DistinctOnEventName(t *testing.T) {
	a := pollLeaseKey("alice", "alert.fired", nil)
	b := pollLeaseKey("alice", "alert.cleared", nil)
	if a == b {
		t.Errorf("pollLeaseKey collided across event names: %q", a)
	}
}

// TestRegisterPoll_TouchesLeaseTable verifies the integration: a real
// events/poll dispatch goes through Touch on the configured lease
// table. Uses the OnCreate hook as a sentinel — if Touch isn't called
// from registerPoll, we never observe the create.
func TestRegisterPoll_TouchesLeaseTable(t *testing.T) {
	var creates atomic.Int32
	leases := NewPollLeaseTable(
		WithPollLeaseSweepInterval(time.Hour),
		WithPollLeaseOnCreate(func(_, _ string, _ map[string]any) { creates.Add(1) }),
	)
	defer leases.Close()

	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:        "poll.lease.touch.test",
		Description: "verifies registerPoll → Touch wiring",
		Delivery:    []string{"poll"},
	})
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Server:                   srv,
		PollLeases:               leases,
		UnsafeAnonymousPrincipal: "test-anon",
	})
	finishInitHandshake(t, srv)

	// Two polls for the same tuple → one create; a different params
	// tuple → second create. This proves both that Touch ran (we'd
	// see zero otherwise) and that the same canonical key is being
	// computed (we'd see two creates for the first pair otherwise).
	for i := 0; i < 2; i++ {
		dispatchPoll(t, srv, "poll.lease.touch.test", map[string]any{"sev": "high"})
	}
	if got := creates.Load(); got != 1 {
		t.Fatalf("after two identical polls: creates=%d, want 1", got)
	}
	dispatchPoll(t, srv, "poll.lease.touch.test", map[string]any{"sev": "low"})
	if got := creates.Load(); got != 2 {
		t.Fatalf("after distinct-params poll: creates=%d, want 2", got)
	}
}

// finishInitHandshake runs the two-step initialize / initialized
// handshake so the server's dispatcher will accept non-init methods.
// Existing tests inline this; pulled here for readability.
func finishInitHandshake(t *testing.T, srv *server.Server) {
	t.Helper()
	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`0`),
		Method:  "initialize",
		Params:  core.NewRawJSON(initParams),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "initialize should succeed; got %+v", resp.Error)
	_, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	require.NoError(t, err)
}

// dispatchPoll invokes events/poll with the given event name and
// params via the server's normal dispatch path.
func dispatchPoll(t *testing.T, srv *server.Server, name string, params map[string]any) {
	t.Helper()
	body := map[string]any{"name": name}
	if params != nil {
		body["arguments"] = params
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "events/poll",
		Params:  core.NewRawJSON(raw),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "events/poll returned error: %+v", resp.Error)
}
