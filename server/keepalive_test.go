package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
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

	// Wait for 3 failures + buffer (3 * 50ms = 150ms + margin)
	time.Sleep(200 * time.Millisecond)

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
		interval:    20 * time.Millisecond,
		maxFailures: 3, // need 3 consecutive failures
		requestFunc: alternatingRequest,
		onDeath:     func() { deathCalled.Store(true) },
	}
	ka.start()
	defer ka.stop()

	// Run long enough for multiple cycles. Since we succeed every 3rd call,
	// we never hit 3 consecutive failures.
	time.Sleep(150 * time.Millisecond)

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
	time.Sleep(80 * time.Millisecond)
	ka.stop()

	// Wait to ensure no deferred onDeath fires
	time.Sleep(100 * time.Millisecond)

	if deathCalled.Load() {
		t.Error("onDeath should NOT be called after stop()")
	}
}

// TestKeepaliveMethodNotFoundDoesNotKillSession verifies that a client
// responding with method-not-found (-32601) to a ping request does NOT
// count as a keepalive failure. Per MCP spec, ping is optional — a
// method-not-found response proves the connection is alive.
func TestKeepaliveMethodNotFoundDoesNotKillSession(t *testing.T) {
	var deathCalled atomic.Bool

	// requestFunc that always returns method-not-found (simulates client
	// that doesn't implement ping).
	methodNotFoundRequest := func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		return nil, &ServerRequestError{
			Code:    core.ErrCodeMethodNotFound,
			Message: "Method not found",
		}
	}

	ka := &sessionKeepalive{
		interval:    30 * time.Millisecond,
		maxFailures: 2, // low threshold — would trigger quickly with real failures
		requestFunc: methodNotFoundRequest,
		onDeath:     func() { deathCalled.Store(true) },
	}
	ka.start()
	defer ka.stop()

	// Run long enough for many ping cycles. None should count as failures.
	time.Sleep(150 * time.Millisecond)

	if deathCalled.Load() {
		t.Error("onDeath should NOT be called when client returns method-not-found")
	}
}

// TestIsMethodNotFound verifies the IsMethodNotFound helper correctly
// identifies ServerRequestError with code -32601.
func TestIsMethodNotFound(t *testing.T) {
	t.Run("method not found", func(t *testing.T) {
		err := &ServerRequestError{Code: core.ErrCodeMethodNotFound, Message: "not found"}
		if !IsMethodNotFound(err) {
			t.Error("expected IsMethodNotFound to return true")
		}
	})
	t.Run("other error code", func(t *testing.T) {
		err := &ServerRequestError{Code: core.ErrCodeInternal, Message: "internal"}
		if IsMethodNotFound(err) {
			t.Error("expected IsMethodNotFound to return false for non-method-not-found")
		}
	})
	t.Run("non-ServerRequestError", func(t *testing.T) {
		err := context.DeadlineExceeded
		if IsMethodNotFound(err) {
			t.Error("expected IsMethodNotFound to return false for non-ServerRequestError")
		}
	})
}
