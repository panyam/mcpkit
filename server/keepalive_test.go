package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// TestSessionKeepaliveDetectsDeadClient verifies that the server-side
// keepalive goroutine detects a dead client after maxFailures consecutive
// ping timeouts and calls onDeath to clean up the session.
func TestSessionKeepaliveDetectsDeadClient(t *testing.T) {
	var deathCalled atomic.Bool

	// requestFunc that always fails (simulates dead client)
	failingRequest := func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		return nil, context.DeadlineExceeded
	}

	ka := &sessionKeepalive{
		interval:    50 * time.Millisecond,
		maxFailures: 3,
		requestFunc: failingRequest,
		onDeath:     func() { deathCalled.Store(true) },
	}
	ka.start()

	// Wait for 3 failures + some buffer (3 * 50ms = 150ms, add margin)
	time.Sleep(300 * time.Millisecond)

	if !deathCalled.Load() {
		t.Error("expected onDeath to be called after max failures")
	}
}

// TestSessionKeepaliveResetsOnSuccess verifies that a successful pong
// response resets the failure counter, preventing premature session death
// when intermittent failures occur.
func TestSessionKeepaliveResetsOnSuccess(t *testing.T) {
	var deathCalled atomic.Bool
	var callCount atomic.Int64

	// Alternate: fail twice, then succeed, repeat
	alternatingRequest := func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		n := callCount.Add(1)
		if n%3 != 0 { // fail 2 out of 3
			return nil, context.DeadlineExceeded
		}
		return json.RawMessage(`{}`), nil // success resets counter
	}

	ka := &sessionKeepalive{
		interval:    30 * time.Millisecond,
		maxFailures: 3, // need 3 consecutive failures
		requestFunc: alternatingRequest,
		onDeath:     func() { deathCalled.Store(true) },
	}
	ka.start()
	defer ka.stop()

	// Run long enough for multiple cycles. Since we succeed every 3rd call,
	// we never hit 3 consecutive failures.
	time.Sleep(300 * time.Millisecond)

	if deathCalled.Load() {
		t.Error("onDeath should NOT be called when failures are intermittent")
	}
}

// TestSessionKeepaliveStopPreventsDeathCallback verifies that calling stop()
// on the keepalive prevents further ping attempts and does not call onDeath,
// even if there were pending failures.
func TestSessionKeepaliveStopPreventsDeathCallback(t *testing.T) {
	var deathCalled atomic.Bool

	failingRequest := func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		return nil, context.DeadlineExceeded
	}

	ka := &sessionKeepalive{
		interval:    50 * time.Millisecond,
		maxFailures: 10, // high threshold — won't reach naturally
		requestFunc: failingRequest,
		onDeath:     func() { deathCalled.Store(true) },
	}
	ka.start()

	// Let it run briefly, then stop
	time.Sleep(100 * time.Millisecond)
	ka.stop()

	// Wait to ensure no deferred onDeath fires
	time.Sleep(200 * time.Millisecond)

	if deathCalled.Load() {
		t.Error("onDeath should NOT be called after stop()")
	}
}
