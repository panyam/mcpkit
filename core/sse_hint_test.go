package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestEmitSSERetry_NoSessionContextIsNoop verifies that calling EmitSSERetry
// outside a session context (e.g., from an in-process test helper) does not
// panic and returns nil. Handlers should be able to call this unconditionally
// without branching on transport type.
func TestEmitSSERetry_NoSessionContextIsNoop(t *testing.T) {
	if err := EmitSSERetry(context.Background(), 5*time.Second); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

// TestEmitSSERetry_SessionWithoutSSEHintIsNoop verifies that a session context
// without an SSE retry emitter (the normal case for stdio/in-process/
// Streamable HTTP JSON transports) silently drops the hint. This is the
// "handler doesn't care what transport it runs on" property: EmitSSERetry
// is safe to call from any handler.
func TestEmitSSERetry_SessionWithoutSSEHintIsNoop(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)

	if err := EmitSSERetry(ctx, 5*time.Second); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

// TestEmitSSERetry_SessionWithSSEHintDelivers verifies that when a transport
// has wired in an SSE retry emitter (via SetSSERetryHint), calling
// EmitSSERetry forwards the millisecond count to the emitter. The transport
// layer owns the actual wire-level write; this test just verifies the
// routing.
func TestEmitSSERetry_SessionWithSSEHintDelivers(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)

	var captured []int
	ctx = SetSSERetryHint(ctx, func(ms int) {
		captured = append(captured, ms)
	})

	if err := EmitSSERetry(ctx, 30*time.Second); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := EmitSSERetry(ctx, 500*time.Millisecond); err != nil {
		t.Fatalf("emit: %v", err)
	}

	if len(captured) != 2 {
		t.Fatalf("captured = %v, want 2 elements", captured)
	}
	if captured[0] != 30000 {
		t.Errorf("captured[0] = %d, want 30000", captured[0])
	}
	if captured[1] != 500 {
		t.Errorf("captured[1] = %d, want 500", captured[1])
	}
}

// TestEmitSSERetry_NegativeAndZeroDropped verifies that non-positive durations
// are silently dropped. This guards against two footguns:
//   - a zero duration from a buggy caller would tell clients to reconnect
//     immediately, causing a thundering herd
//   - a negative duration is never valid per the SSE spec
func TestEmitSSERetry_NegativeAndZeroDropped(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)

	var called int32
	ctx = SetSSERetryHint(ctx, func(ms int) {
		atomic.AddInt32(&called, 1)
	})

	_ = EmitSSERetry(ctx, 0)
	_ = EmitSSERetry(ctx, -5*time.Second)
	_ = EmitSSERetry(ctx, 500*time.Microsecond) // rounds down to 0 ms

	if n := atomic.LoadInt32(&called); n != 0 {
		t.Errorf("hint fired %d times, want 0 (non-positive + sub-ms inputs must be dropped)", n)
	}
}

// TestSetSSERetryHint_NoSessionContextIsNoop verifies that SetSSERetryHint
// called on a bare context (no session) is a no-op — it returns the same
// context without attaching anything. Defensive: transport setup code may
// call SetSSERetryHint before (or after) ContextWithSession and the failure
// mode should be silent drop, not panic.
func TestSetSSERetryHint_NoSessionContextIsNoop(t *testing.T) {
	ctx := context.Background()
	out := SetSSERetryHint(ctx, func(ms int) {})
	if out != ctx {
		t.Errorf("SetSSERetryHint on empty context returned different context; expected same")
	}
}
