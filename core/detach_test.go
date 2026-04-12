package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestDetachFromClient_PreservesSessionState verifies that DetachFromClient
// preserves the session context (EmitLog, EmitSSERetry, Sample capabilities)
// — only the cancellation signal is removed. Session-dependent APIs must
// keep working after detach, otherwise a detached handler can't log, emit
// progress, or send retry hints.
func TestDetachFromClient_PreservesSessionState(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	lvl := LogDebug
	logLevel.Store(&lvl)

	var notified []string
	notify := NotifyFunc(func(method string, params any) {
		notified = append(notified, method)
	})

	ctx := ContextWithSession(context.Background(), notify, nil, &logLevel, nil, nil)

	var retryCaptured []int
	ctx = SetSSERetryHint(ctx, func(ms int) {
		retryCaptured = append(retryCaptured, ms)
	})

	// Detach.
	ctx = DetachFromClient(ctx)

	// Session APIs must still work.
	EmitLog(ctx, LogInfo, "test", "after detach")
	_ = EmitSSERetry(ctx, 5*time.Second)

	if len(notified) != 1 || notified[0] != "notifications/message" {
		t.Errorf("EmitLog after detach: notified = %v, want [notifications/message]", notified)
	}
	if len(retryCaptured) != 1 || retryCaptured[0] != 5000 {
		t.Errorf("EmitSSERetry after detach: captured = %v, want [5000]", retryCaptured)
	}
}

// TestDetachFromClient_NotCancelledOnParentCancel verifies the core
// contract: after DetachFromClient, cancelling the parent context does
// NOT cancel the detached context. This is the "tool keeps running after
// client disconnects" guarantee.
func TestDetachFromClient_NotCancelledOnParentCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())

	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(parent, nil, nil, &logLevel, nil, nil)
	ctx = DetachFromClient(ctx)

	// Cancel the parent (simulates HTTP request context cancelled by
	// client disconnect).
	cancel()

	// The detached context must NOT be done.
	select {
	case <-ctx.Done():
		t.Fatal("detached context was cancelled by parent — DetachFromClient failed to decouple")
	default:
		// Expected: ctx.Done() is nil or not signalled.
	}
}

// TestDetachFromClient_StripsTimeout verifies that DetachFromClient
// removes inherited timeouts. A tool that detaches is long-running by
// definition — the per-tool timeout from ToolDef.Timeout or
// WithToolTimeout should not apply. Handlers that need a deadline after
// detach must set their own.
func TestDetachFromClient_StripsTimeout(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(parent, nil, nil, &logLevel, nil, nil)
	ctx = DetachFromClient(ctx)

	// Wait longer than the parent's timeout.
	time.Sleep(10 * time.Millisecond)

	// The detached context should still be alive.
	select {
	case <-ctx.Done():
		t.Fatal("detached context was cancelled by parent timeout — DetachFromClient should strip it")
	default:
		// Expected.
	}
}

// TestDetachFromClient_ChildTimeoutStillWorks verifies that after
// detaching, the handler can apply its own timeout and it works normally.
// This is the recommended pattern: detach first, then set a long deadline.
func TestDetachFromClient_ChildTimeoutStillWorks(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(parent, nil, nil, &logLevel, nil, nil)
	ctx = DetachFromClient(ctx)

	// Apply a new short timeout on the detached context.
	ctx, childCancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer childCancel()

	// Wait for the child timeout to fire.
	<-ctx.Done()

	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
}

// TestDetachFromClient_BareContextIsNoop verifies that calling
// DetachFromClient on a context without a session (no ContextWithSession)
// does not panic and returns a usable context. Defensive: a handler might
// call DetachFromClient unconditionally without checking whether it's
// running under a session-aware transport.
func TestDetachFromClient_BareContextIsNoop(t *testing.T) {
	ctx := DetachFromClient(context.Background())
	if ctx == nil {
		t.Fatal("DetachFromClient returned nil")
	}
	// No-ops should still be safe.
	EmitLog(ctx, LogInfo, "test", "nothing happens")
	_ = EmitSSERetry(ctx, 1*time.Second)
}
