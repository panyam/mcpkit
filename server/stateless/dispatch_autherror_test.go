package stateless

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// A middleware short-circuit raised inside InvokeWithMiddleware must propagate
// out of Dispatch as the second return value, unfolded — NOT collapsed into a
// generic -32603 *core.Response. The transport relies on this to emit HTTP 403
// + WWW-Authenticate via writeAuthError (issue 815). Before the fix, Dispatch
// had no error return and the backend folded the AuthError into -32603, which
// HTTPStatusForCode maps to HTTP 200 — silently losing the scope challenge.
func TestDispatch_MiddlewareAuthErrorPropagates(t *testing.T) {
	authErr := &core.AuthError{
		Code:            http.StatusForbidden,
		Message:         "insufficient scope",
		WWWAuthenticate: `Bearer error="insufficient_scope", scope="docs:write"`,
	}
	// Method "tools/call" routes through the backend's middleware path.
	d := New(&fakeBackend{authErr: authErr})

	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  validMetaParams(t),
	}
	resp, err := d.Dispatch(context.Background(), req)

	if err == nil {
		t.Fatalf("Dispatch err = nil; want the middleware *core.AuthError to propagate")
	}
	var got *core.AuthError
	if !errors.As(err, &got) {
		t.Fatalf("Dispatch err = %T (%v); want *core.AuthError", err, err)
	}
	if got.Code != http.StatusForbidden {
		t.Errorf("AuthError.Code = %d, want 403", got.Code)
	}
	if got.WWWAuthenticate != authErr.WWWAuthenticate {
		t.Errorf("AuthError.WWWAuthenticate = %q, want %q", got.WWWAuthenticate, authErr.WWWAuthenticate)
	}
	// The response must be nil on the error path — a non-nil -32603 body here
	// would mean the transport writes HTTP 200 + a generic JSON-RPC error
	// instead of the auth challenge.
	if resp != nil {
		t.Errorf("Dispatch resp = %+v; want nil on the error path", resp)
	}
}

// The custom-method (default) branch routes through the same middleware path,
// so an AuthError there must propagate identically.
func TestDispatch_MiddlewareAuthErrorPropagatesForCustomMethod(t *testing.T) {
	authErr := &core.AuthError{Code: http.StatusForbidden, Message: "denied"}
	d := New(&fakeBackend{authErr: authErr})

	req := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "events/poll",
		Params:  validMetaParams(t),
	}
	resp, err := d.Dispatch(context.Background(), req)

	if err == nil {
		t.Fatalf("Dispatch err = nil; want the custom-method middleware AuthError to propagate")
	}
	if resp != nil {
		t.Errorf("Dispatch resp = %+v; want nil on the error path", resp)
	}
}
