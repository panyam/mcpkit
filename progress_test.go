package mcpkit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestEmitProgressSendsNotification verifies that EmitProgress sends a correctly
// structured notifications/progress notification through the session's NotifyFunc.
// The notification must include the progress token, current progress, and total.
func TestEmitProgressSendsNotification(t *testing.T) {
	var gotMethod string
	var gotParams any
	notify := func(method string, params any) {
		gotMethod = method
		gotParams = params
	}

	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	EmitProgress(ctx, "token-1", 50, 100, "halfway")

	if gotMethod != "notifications/progress" {
		t.Errorf("method = %q, want notifications/progress", gotMethod)
	}
	pn, ok := gotParams.(ProgressNotification)
	if !ok {
		t.Fatalf("params type = %T, want ProgressNotification", gotParams)
	}
	if pn.ProgressToken != "token-1" {
		t.Errorf("progressToken = %v, want token-1", pn.ProgressToken)
	}
	if pn.Progress != 50 {
		t.Errorf("progress = %v, want 50", pn.Progress)
	}
	if pn.Total != 100 {
		t.Errorf("total = %v, want 100", pn.Total)
	}
	if pn.Message != "halfway" {
		t.Errorf("message = %q, want halfway", pn.Message)
	}
}

// TestEmitProgressNilToken verifies that EmitProgress is a no-op when the progress
// token is nil (the client did not request progress reporting). This prevents
// spurious notifications when tools always call EmitProgress unconditionally.
func TestEmitProgressNilToken(t *testing.T) {
	called := false
	notify := func(method string, params any) {
		called = true
	}

	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	EmitProgress(ctx, nil, 50, 100, "should not send")

	if called {
		t.Error("EmitProgress called notify with nil token")
	}
}

// TestEmitProgressNoSession verifies that calling EmitProgress without a session
// context is a safe no-op. Tool handlers may be tested outside a transport context.
func TestEmitProgressNoSession(t *testing.T) {
	// Should not panic
	EmitProgress(context.Background(), "token", 0, 100, "test")
}

// TestEmitProgressMultiple verifies that multiple progress notifications can be
// sent in sequence with monotonically increasing progress values, matching the
// typical usage pattern in long-running tool handlers.
func TestEmitProgressMultiple(t *testing.T) {
	var mu sync.Mutex
	var received []ProgressNotification

	notify := func(method string, params any) {
		pn := params.(ProgressNotification)
		mu.Lock()
		received = append(received, pn)
		mu.Unlock()
	}

	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	EmitProgress(ctx, "tok", 0, 100, "start")
	EmitProgress(ctx, "tok", 50, 100, "mid")
	EmitProgress(ctx, "tok", 100, 100, "done")

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 {
		t.Fatalf("got %d notifications, want 3", len(received))
	}
	wantProgress := []float64{0, 50, 100}
	for i, want := range wantProgress {
		if received[i].Progress != want {
			t.Errorf("received[%d].Progress = %v, want %v", i, received[i].Progress, want)
		}
	}
}

// TestEmitProgressNumericToken verifies that EmitProgress works with numeric
// progress tokens (the spec allows both string and integer tokens).
func TestEmitProgressNumericToken(t *testing.T) {
	var gotToken any
	notify := func(method string, params any) {
		gotToken = params.(ProgressNotification).ProgressToken
	}

	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	EmitProgress(ctx, float64(42), 10, 100, "")

	if gotToken != float64(42) {
		t.Errorf("token = %v (%T), want 42 (float64)", gotToken, gotToken)
	}
}
