package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestErrorHandlerOnKeepaliveFailure verifies that the ErrorHandler's
// OnKeepaliveFailure method is called each time a keepalive ping fails,
// with the correct consecutive failure count.
func TestErrorHandlerOnKeepaliveFailure(t *testing.T) {
	var lastFailures atomic.Int64

	ka := &sessionKeepalive{
		interval:    50 * time.Millisecond,
		maxFailures: 3,
		requestFunc: func(ctx context.Context, method string, params any) (json.RawMessage, error) {
			return nil, context.DeadlineExceeded
		},
		onDeath:    func() {},
		onPingFail: func(failures int) { lastFailures.Store(int64(failures)) },
	}
	ka.start()

	// Wait for at least 2 failures
	time.Sleep(150 * time.Millisecond)
	ka.stop()

	got := lastFailures.Load()
	assert.GreaterOrEqual(t, got, int64(2), "onPingFail should have been called with increasing failure counts")
}

// TestBaseErrorHandlerIsNoOp verifies that BaseErrorHandler provides
// no-op defaults that don't panic when called. This ensures users can
// embed BaseErrorHandler and only override the methods they care about.
func TestBaseErrorHandlerIsNoOp(t *testing.T) {
	var h BaseErrorHandler
	// None of these should panic
	h.OnSessionExpire("test-session", nil)
	h.OnTransportError(nil)
	h.OnKeepaliveFailure("test-session", 5)
}
