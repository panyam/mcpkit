package client

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyConnectErr(t *testing.T) {
	if got := classifyConnectErr(&ClientAuthError{StatusCode: 401, Message: "nope"}); got != StateNeedsLogin {
		t.Errorf("401 -> %v, want needs-login", got)
	}
	// a wrapped auth error still classifies as needs-login
	wrapped := fmt.Errorf("connect: %w", &ClientAuthError{StatusCode: 403})
	if got := classifyConnectErr(wrapped); got != StateNeedsLogin {
		t.Errorf("wrapped 403 -> %v, want needs-login", got)
	}
	// anything else is retryable
	if got := classifyConnectErr(errors.New("connection refused")); got != StateFailed {
		t.Errorf("generic -> %v, want failed", got)
	}
}
