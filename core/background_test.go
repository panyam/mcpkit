package core

import (
	"context"
	"encoding/json"
	"testing"
)

// TestDetachForBackgroundDefault verifies that without a strategy set,
// DetachForBackground behaves like context.WithoutCancel — preserves
// values but detaches cancellation.
func TestDetachForBackgroundDefault(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "hello")
	ctx, cancel := context.WithCancel(ctx)

	bgCtx := DetachForBackground(ctx)

	// Values should be preserved.
	if bgCtx.Value(key{}) != "hello" {
		t.Error("expected value to be preserved")
	}

	// Cancelling the parent should NOT cancel the background context.
	cancel()
	if bgCtx.Err() != nil {
		t.Error("background context should not be cancelled when parent is cancelled")
	}
}

// TestDetachForBackgroundWithStrategy verifies that a custom strategy
// is called and its result is used.
func TestDetachForBackgroundWithStrategy(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "original")

	// Strategy that adds an extra value.
	type extraKey struct{}
	ctx = SetDetachStrategy(ctx, func(c context.Context) context.Context {
		c = context.WithoutCancel(c)
		return context.WithValue(c, extraKey{}, "injected")
	})

	bgCtx := DetachForBackground(ctx)

	// Original value preserved.
	if bgCtx.Value(key{}) != "original" {
		t.Error("expected original value to be preserved")
	}

	// Strategy-injected value present.
	if bgCtx.Value(extraKey{}) != "injected" {
		t.Error("expected strategy-injected value")
	}
}

// TestDetachForBackgroundStrategyReplacesRequestFunc verifies the pattern
// used by the server: the strategy replaces the session's requestFunc with
// one that uses the persistent push (simulated here with a mock).
func TestDetachForBackgroundStrategyReplacesRequestFunc(t *testing.T) {
	// Simulate: original context has a "dead" request func from POST response.
	deadCalled := false
	deadReqFunc := RequestFunc(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		deadCalled = true
		return nil, nil
	})

	// Create session context with the dead request func.
	ctx := ContextWithSession(context.Background(), nil, deadReqFunc, nil, nil, nil)

	// Simulate: server sets a strategy that replaces with a "live" request func.
	liveCalled := false
	liveReqFunc := RequestFunc(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		liveCalled = true
		return json.RawMessage(`{"action":"accept"}`), nil
	})

	ctx = SetDetachStrategy(ctx, func(c context.Context) context.Context {
		c = context.WithoutCancel(c)
		return ReplaceSessionRequestFunc(c, liveReqFunc)
	})

	// Detach for background.
	bgCtx := DetachForBackground(ctx)

	// Call request func from background context — should use live, not dead.
	sc := sessionFromContext(bgCtx)
	if sc == nil || sc.request == nil {
		t.Fatal("expected session with request func in background context")
	}
	sc.request(bgCtx, "test/method", nil)

	if deadCalled {
		t.Error("dead request func should NOT have been called")
	}
	if !liveCalled {
		t.Error("live request func should have been called")
	}
}
