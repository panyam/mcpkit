package core

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestNotifyResourceUpdated_DispatchesToSubscribers verifies that
// NotifyResourceUpdated(ctx, uri) calls the notifyResourceUpdated
// function installed on the session context. This is the handler-facing
// wrapper that routes through the subscription registry to fan out to
// all subscribed sessions (not just the caller's). Issue #208.
func TestNotifyResourceUpdated_DispatchesToSubscribers(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)

	var capturedURI string
	ctx = SetNotifyResourceUpdated(ctx, func(uri string) {
		capturedURI = uri
	})

	NotifyResourceUpdated(ctx, "widgets/123")

	if capturedURI != "widgets/123" {
		t.Errorf("capturedURI = %q, want widgets/123", capturedURI)
	}
}

// TestNotifyResourceUpdated_NoSessionIsNoop verifies that calling
// NotifyResourceUpdated without a session context does not panic.
// Handlers should be free to call this unconditionally.
func TestNotifyResourceUpdated_NoSessionIsNoop(t *testing.T) {
	// Should not panic.
	NotifyResourceUpdated(context.Background(), "widgets/123")
}

// TestNotifyResourceUpdated_NoSubscriptionsIsNoop verifies that calling
// NotifyResourceUpdated when subscriptions are not enabled (no
// notifyResourceUpdated function installed) is a silent no-op.
func TestNotifyResourceUpdated_NoSubscriptionsIsNoop(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := ContextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)
	// No SetNotifyResourceUpdated call — simulates WithSubscriptions not enabled.
	NotifyResourceUpdated(ctx, "widgets/123")
	// Should not panic. No assertion needed — the test is that it doesn't crash.
}

// TestSetNotifyResourceUpdated_NoSessionIsNoop verifies that
// SetNotifyResourceUpdated on a bare context doesn't panic.
// With no session (the stateless wire) the notifier rides on ctx (#1625).
func TestSetNotifyResourceUpdated_NoSessionRidesOnCtx(t *testing.T) {
	var got string
	ctx := SetNotifyResourceUpdated(context.Background(), func(uri string) { got = uri })
	NotifyResourceUpdated(ctx, "test://x")
	if got != "test://x" {
		t.Errorf("notifier saw %q, want test://x", got)
	}
}
