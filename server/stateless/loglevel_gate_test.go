package stateless

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// SEP-2575 Bucket 5 — "no log without logLevel" invariant.
//
// The spec invariant: a stateless server MUST NOT emit
// notifications/message during a tools/call unless the per-request
// _meta carries io.modelcontextprotocol/logLevel (the client opt-in).
// The fixture's test_logging_tool implements this by reading the
// validated RequestMeta from ctx and only calling EmitLog when the
// LogLevel field is non-empty.
//
// At the Go level the dispatcher does not enforce this — handlers do.
// What the dispatcher MUST do is thread the validated _meta envelope
// through ctx so handlers can read meta.LogLevel reliably. This test
// pins that contract: the per-request meta is reachable via
// RequestMetaFromContext, and meta.LogLevel reflects what the client
// stamped.
//
// The "actually emits / actually doesn't emit" behavior is exercised
// end-to-end by the upstream conformance scenario's
// ServerNoLogWithoutLogLevel check (driven via make testconf-stateless).
// That scenario opens a tools/call stream and asserts the absence of
// notifications/message frames when logLevel is omitted.

func TestRequestMeta_LogLevelThreadingExposedToHandlers(t *testing.T) {
	cases := []struct {
		name     string
		meta     string
		wantLvl  string
	}{
		{
			"absent logLevel — handler sees empty",
			`{
				"io.modelcontextprotocol/protocolVersion": "DRAFT-2026-v1",
				"io.modelcontextprotocol/clientInfo": {"name": "c", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": {}
			}`,
			"",
		},
		{
			"opt-in info — handler sees info",
			`{
				"io.modelcontextprotocol/protocolVersion": "DRAFT-2026-v1",
				"io.modelcontextprotocol/clientInfo": {"name": "c", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": {},
				"io.modelcontextprotocol/logLevel": "info"
			}`,
			"info",
		},
		{
			"opt-in debug — handler sees debug",
			`{
				"io.modelcontextprotocol/protocolVersion": "DRAFT-2026-v1",
				"io.modelcontextprotocol/clientInfo": {"name": "c", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": {},
				"io.modelcontextprotocol/logLevel": "debug"
			}`,
			"debug",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The handler-visible view: a ToolHandler running under
			// Dispatcher.Dispatch reads meta via
			// stateless.RequestMetaFromContext(ctx).
			var captured *core.RequestMeta

			d := New(&fakeBackend{
				tool: func(name string) (core.ToolDef, core.ToolHandler, bool) {
					if name == "spy_log_level" {
						return core.ToolDef{Name: name},
							func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
								captured = RequestMetaFromContext(ctx)
								return core.TextResult("ok"), nil
							}, true
					}
					return core.ToolDef{}, nil, false
				},
			})

			params := json.RawMessage(`{"name":"spy_log_level","arguments":{},"_meta":` + tc.meta + `}`)
			req := &core.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage("1"),
				Method:  "tools/call",
				Params:  params,
			}
			resp := d.Dispatch(context.Background(), req)
			if resp == nil || resp.Error != nil {
				t.Fatalf("tools/call errored: %+v", resp)
			}
			if captured == nil {
				t.Fatal("handler did not see a RequestMeta in ctx")
			}
			if captured.LogLevel != tc.wantLvl {
				t.Errorf("captured.LogLevel = %q, want %q", captured.LogLevel, tc.wantLvl)
			}
		})
	}
}
