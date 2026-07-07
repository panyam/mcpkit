package server

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// Test_Issue421_DuplicateInitializeRejected asserts that once a session has
// negotiated a protocol version, a second initialize is rejected with -32600
// and the existing session state (version, client identity) is preserved.
func Test_Issue421_DuplicateInitializeRejected(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "s", Version: "1"})

	first := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"alice","version":"1.0"}}`),
	})
	if first == nil || first.Error != nil {
		t.Fatalf("first initialize should succeed, got %+v", first)
	}
	if d.negotiatedVersion != "2025-03-26" {
		t.Fatalf("expected negotiated 2025-03-26, got %q", d.negotiatedVersion)
	}
	d.Dispatch(context.Background(), &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})

	// Second initialize: different version + different client identity.
	second := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"mallory","version":"9.9"}}`),
	})
	if second == nil || second.Error == nil || second.Error.Code != core.ErrCodeInvalidRequest {
		t.Fatalf("expected -32600 InvalidRequest on duplicate initialize, got %+v", second)
	}
	// State must be untouched.
	if d.negotiatedVersion != "2025-03-26" {
		t.Errorf("negotiatedVersion changed to %q; must be preserved", d.negotiatedVersion)
	}
	if d.clientInfo.Name != "alice" {
		t.Errorf("clientInfo overwritten to %q; must be preserved", d.clientInfo.Name)
	}
}

// Test_Issue421_ReinitializeAllowedWithOption asserts the opt-in escape hatch:
// with allowReinitialize set, a second initialize re-negotiates and updates state.
func Test_Issue421_ReinitializeAllowedWithOption(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "s", Version: "1"})
	d.allowReinitialize = true

	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"alice","version":"1.0"}}`),
	})
	second := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"alice2","version":"2.0"}}`),
	})
	if second == nil || second.Error != nil {
		t.Fatalf("re-initialize should succeed with allowReinitialize, got %+v", second)
	}
	if d.negotiatedVersion != "2024-11-05" || d.clientInfo.Name != "alice2" {
		t.Errorf("re-initialize should update state, got version=%q client=%q", d.negotiatedVersion, d.clientInfo.Name)
	}
}
