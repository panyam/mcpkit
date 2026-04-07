package server

import (
	core "github.com/panyam/mcpkit/core"
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// initDispatcher performs the full MCP initialization handshake on a dispatcher
// (initialize + notifications/initialized) so subsequent tool calls are accepted.
func initDispatcher(d *Dispatcher) {
	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`0`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
}

func testDispatcher() *Dispatcher {
	d := NewDispatcher(core.ServerInfo{Name: "test-server", Version: "1.0.0"})
	d.RegisterTool(
		core.ToolDef{
			Name:        "echo",
			Description: "Echoes the input",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
				"required": []string{"message"},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			if err := req.Bind(&args); err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			return core.TextResult("echo: " + args.Message), nil
		},
	)
	initDispatcher(d)
	return d
}

func TestDispatchInitialize(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatal("serverInfo not a map")
	}
	if info["name"] != "test-server" {
		t.Errorf("serverInfo.name = %v, want test-server", info["name"])
	}
	if info["version"] != "1.0.0" {
		t.Errorf("serverInfo.version = %v, want 1.0.0", info["version"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("capabilities not a map")
	}
	if _, ok := caps["tools"]; !ok {
		t.Error("capabilities missing tools")
	}
}

func TestDispatchNotification(t *testing.T) {
	d := testDispatcher()
	for _, method := range []string{"notifications/initialized", "initialized"} {
		resp := d.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0",
			Method:  method,
		})
		if resp != nil {
			t.Errorf("method %q: expected nil response, got %+v", method, resp)
		}
	}
}

func TestDispatchPing(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`99`),
		Method:  "ping",
	})
	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if string(resp.ID) != "99" {
		t.Errorf("ID = %s, want 99", resp.ID)
	}
}

func TestDispatchToolsList(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "echo" {
		t.Errorf("tool name = %q, want echo", result.Tools[0].Name)
	}
}

func TestDispatchToolsCall(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"message":"hello"}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result core.ToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Error("result.IsError = true, want false")
	}
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	if result.Content[0].Text != "echo: hello" {
		t.Errorf("text = %q, want echo: hello", result.Content[0].Text)
	}
}

func TestDispatchToolsCallUnknown(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`4`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"nonexistent","arguments":{}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
}

func TestDispatchToolsCallBadParams(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`5`),
		Method:  "tools/call",
		Params:  json.RawMessage(`not json`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
}

func TestDispatchMethodNotFound(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`6`),
		Method:  "unknown/method",
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != core.ErrCodeMethodNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeMethodNotFound)
	}
}

func TestDispatchNullID(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      nil,
		Method:  "ping",
	})
	if resp == nil {
		t.Fatal("response is nil")
	}
	if string(resp.ID) != "null" {
		t.Errorf("ID = %s, want null", resp.ID)
	}
}

// TestDispatchToolsCallHandlerError verifies that when a core.ToolHandler returns a Go error,
// the dispatcher wraps it as a JSON-RPC success with isError: true in the tool result,
// NOT as a JSON-RPC error response. Per the MCP spec, JSON-RPC errors are reserved for
// protocol-level failures (bad params, unknown tool). Tool execution failures use isError.
func TestDispatchToolsCallHandlerError(t *testing.T) {
	d := testDispatcher()
	// Register a tool that always returns a Go error
	d.RegisterTool(
		core.ToolDef{Name: "failing", Description: "always fails"},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.ToolResult{}, fmt.Errorf("something broke")
		},
	)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`10`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"failing","arguments":{}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	// Must NOT be a JSON-RPC error — tool failures are reported via isError in the result
	if resp.Error != nil {
		t.Fatalf("got JSON-RPC error (code %d: %s), want JSON-RPC success with isError in result",
			resp.Error.Code, resp.Error.Message)
	}

	var result core.ToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Error("result.IsError = false, want true")
	}
	if len(result.Content) == 0 {
		t.Fatal("result has no content")
	}
	if result.Content[0].Text == "" {
		t.Error("error content text is empty")
	}
}

func TestDispatchToolOrder(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		d.RegisterTool(core.ToolDef{Name: name, Description: name}, func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult(name), nil
		})
	}
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})

	var result struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 3 {
		t.Fatalf("got %d tools, want 3", len(result.Tools))
	}
	// tools/list should preserve registration order
	want := []string{"charlie", "alpha", "bravo"}
	for i, name := range want {
		if result.Tools[i].Name != name {
			t.Errorf("tools[%d].Name = %q, want %q", i, result.Tools[i].Name, name)
		}
	}
}

// TestDispatchInitializeVersion2025 verifies that the server correctly negotiates
// protocol version 2025-11-25 when the client requests it. The server should respond
// with the same version in protocolVersion, confirming mutual support.
func TestDispatchInitializeVersion2025(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["protocolVersion"] != "2025-11-25" {
		t.Errorf("protocolVersion = %v, want 2025-11-25", result["protocolVersion"])
	}
}

// TestDispatchInitializeUnsupportedVersion verifies that the server rejects an unknown
// protocol version with a JSON-RPC error code -32602 (invalid params) and includes
// the list of supported versions in the error data, per the MCP spec.
func TestDispatchInitializeUnsupportedVersion(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"1999-01-01","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error for unsupported version, got success")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
	// core.Error data must contain a "supported" array listing valid versions.
	// Round-trip through JSON to normalize types (the in-memory struct uses
	// concrete Go types, but a real client would see JSON).
	raw, err := json.Marshal(resp.Error.Data)
	if err != nil {
		t.Fatalf("failed to marshal error data: %v", err)
	}
	var data map[string][]string
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("failed to unmarshal error data: %v", err)
	}
	versions := data["supported"]
	if len(versions) < 2 {
		t.Errorf("expected at least 2 supported versions, got %d", len(versions))
	}
}

// TestDispatchInitializeMissingParams verifies that sending initialize with nil params
// returns a JSON-RPC error (invalid params), not a panic or success.
func TestDispatchInitializeMissingParams(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error for missing params")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
}

// TestDispatchInitializeStoresClientInfo verifies that the dispatcher stores the
// client info and capabilities from the initialize request, making them available
// for server-to-client feature detection.
func TestDispatchInitializeStoresClientInfo(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"roots":{"listChanged":true}},"clientInfo":{"name":"my-client","version":"2.0"}}`),
	})

	if d.clientInfo.Name != "my-client" {
		t.Errorf("clientInfo.Name = %q, want my-client", d.clientInfo.Name)
	}
	if d.clientInfo.Version != "2.0" {
		t.Errorf("clientInfo.Version = %q, want 2.0", d.clientInfo.Version)
	}
	if d.negotiatedVersion != "2024-11-05" {
		t.Errorf("negotiatedVersion = %q, want 2024-11-05", d.negotiatedVersion)
	}
}

// TestDispatchBeforeInitialized verifies that the server rejects tool calls when
// initialize has been called but notifications/initialized has not yet been received.
// The MCP spec requires the full initialization handshake before processing requests.
func TestDispatchBeforeInitialized(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterTool(
		core.ToolDef{Name: "echo", Description: "echoes"},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("hi"), nil
		},
	)
	// Send initialize but NOT notifications/initialized
	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error for request before initialized notification")
	}
	if resp.Error.Code != core.ErrCodeInvalidRequest {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidRequest)
	}
}

// TestDispatchToolsCallBeforeAnyInit verifies that tool calls are rejected when
// no initialization has occurred at all (neither initialize nor notifications/initialized).
func TestDispatchToolsCallBeforeAnyInit(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterTool(
		core.ToolDef{Name: "echo", Description: "echoes"},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("hi"), nil
		},
	)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error for request before any initialization")
	}
	if resp.Error.Code != core.ErrCodeInvalidRequest {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidRequest)
	}
}

// TestDispatchPingBeforeInitialized verifies that ping works at any time,
// even before the initialization handshake is complete. The MCP spec allows
// ping as a keepalive mechanism regardless of session state.
func TestDispatchPingBeforeInitialized(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "ping",
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("ping should work before init, got error: %s", resp.Error.Message)
	}
}

// TestDispatchLoggingSetLevel verifies that logging/setLevel accepts a valid log level,
// stores it on the dispatcher, and returns an empty result object. This is the happy path
// for clients configuring the MCP log stream.
func TestDispatchLoggingSetLevel(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`10`),
		Method:  "logging/setLevel",
		Params:  json.RawMessage(`{"level":"warning"}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	// Verify the level was stored
	stored := d.logLevel.Load()
	if stored == nil {
		t.Fatal("logLevel not set")
	}
	if *stored != core.LogWarning {
		t.Errorf("logLevel = %v, want core.LogWarning", *stored)
	}
}

// TestDispatchLoggingSetLevelAllLevels verifies that all 8 syslog-based log levels
// are accepted by logging/setLevel. The MCP spec defines these as: debug, info, notice,
// warning, error, critical, alert, emergency.
func TestDispatchLoggingSetLevelAllLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "notice", "warning", "error", "critical", "alert", "emergency"} {
		t.Run(level, func(t *testing.T) {
			d := testDispatcher()
			resp := d.Dispatch(context.Background(), &core.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage(`1`),
				Method:  "logging/setLevel",
				Params:  json.RawMessage(`{"level":"` + level + `"}`),
			})
			if resp.Error != nil {
				t.Fatalf("logging/setLevel(%q) failed: %s", level, resp.Error.Message)
			}
		})
	}
}

// TestDispatchLoggingSetLevelInvalid verifies that logging/setLevel rejects unknown
// level strings with a JSON-RPC invalid params error (-32602).
func TestDispatchLoggingSetLevelInvalid(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`11`),
		Method:  "logging/setLevel",
		Params:  json.RawMessage(`{"level":"trace"}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown level")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
}

// TestDispatchLoggingSetLevelBeforeInit verifies that logging/setLevel is rejected
// before the initialization handshake completes, consistent with MCP init gating.
func TestDispatchLoggingSetLevelBeforeInit(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "logging/setLevel",
		Params:  json.RawMessage(`{"level":"info"}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error before initialization")
	}
	if resp.Error.Code != core.ErrCodeInvalidRequest {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidRequest)
	}
}

// TestDispatchLoggingCapability verifies that the server advertises the logging
// capability in the initialize response. Clients check this to know whether
// logging/setLevel is supported.
func TestDispatchLoggingCapability(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	if resp.Error != nil {
		t.Fatalf("init failed: %s", resp.Error.Message)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("capabilities not a map")
	}
	if _, ok := caps["logging"]; !ok {
		t.Error("capabilities missing logging")
	}
}

// TestDispatchToolsCallWithProgressToken verifies that when a tools/call request
// includes _meta.progressToken, the token is extracted and populated in the
// core.ToolRequest.ProgressToken field, making it available for core.EmitProgress calls.
func TestDispatchToolsCallWithProgressToken(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	var gotToken any
	d.RegisterTool(
		core.ToolDef{Name: "progress_tool", Description: "captures progress token"},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			gotToken = req.ProgressToken
			return core.TextResult("ok"), nil
		},
	)
	initDispatcher(d)

	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"progress_tool","arguments":{},"_meta":{"progressToken":"my-token"}}`),
	})

	if gotToken != "my-token" {
		t.Errorf("ProgressToken = %v (%T), want my-token", gotToken, gotToken)
	}
}

// TestDispatchToolsCallWithoutProgressToken verifies that when _meta is absent,
// ProgressToken remains nil, so core.EmitProgress is a safe no-op.
func TestDispatchToolsCallWithoutProgressToken(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	var gotToken any = "sentinel"
	d.RegisterTool(
		core.ToolDef{Name: "no_progress", Description: "no progress token"},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			gotToken = req.ProgressToken
			return core.TextResult("ok"), nil
		},
	)
	initDispatcher(d)

	d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"no_progress","arguments":{}}`),
	})

	if gotToken != nil {
		t.Errorf("ProgressToken = %v, want nil", gotToken)
	}
}

// TestDispatchToolsListExtraSchemaFields verifies that extra JSON Schema fields
// beyond the MCP spec minimum (type, properties, required) — such as $schema,
// $defs, $ref, and additionalProperties — are preserved in tools/list responses.
// This guards against regressions where InputSchema might be replaced with a
// typed struct that drops unknown fields.
func TestDispatchToolsListExtraSchemaFields(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test-server", Version: "1.0.0"})
	d.RegisterTool(
		core.ToolDef{
			Name:        "schema_extra",
			Description: "Tool with extra JSON Schema fields",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
				"required":            []string{"name"},
				"additionalProperties": false,
				"$schema":             "http://json-schema.org/draft-07/schema#",
				"$defs": map[string]any{
					"Address": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"street": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Tools))
	}

	schema := result.Tools[0].InputSchema

	// additionalProperties must be preserved as false (boolean).
	if ap, ok := schema["additionalProperties"]; !ok {
		t.Error("additionalProperties missing from schema")
	} else if ap != false {
		t.Errorf("additionalProperties = %v (%T), want false", ap, ap)
	}

	// $schema must be preserved as a string.
	if s, ok := schema["$schema"]; !ok {
		t.Error("$schema missing from schema")
	} else if s != "http://json-schema.org/draft-07/schema#" {
		t.Errorf("$schema = %v, want draft-07 URI", s)
	}

	// $defs must be preserved as a nested object.
	defs, ok := schema["$defs"]
	if !ok {
		t.Fatal("$defs missing from schema")
	}
	defsMap, ok := defs.(map[string]any)
	if !ok {
		t.Fatalf("$defs is %T, want map[string]any", defs)
	}
	if _, ok := defsMap["Address"]; !ok {
		t.Error("$defs.Address missing")
	}
}

// TestToolsListMeta verifies that tools/list preserves the _meta field through
// JSON-RPC dispatch. The _meta mechanism is how MCP extensions attach metadata
// to tools — any extension (apps/ui, future ones) relies on _meta surviving
// the dispatch round-trip. Uses UI metadata as the concrete test payload.
func TestToolsListMeta(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterTool(
		core.ToolDef{
			Name:        "ui_tool",
			Description: "Tool with UI metadata",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://myapp/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel},
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	d.RegisterTool(
		core.ToolDef{
			Name:        "plain_tool",
			Description: "Tool without UI metadata",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}

	// Parse into raw JSON to verify _meta wire format
	var raw struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(raw.Tools))
	}

	// First tool should have _meta.ui.resourceUri
	var tool0 map[string]json.RawMessage
	json.Unmarshal(raw.Tools[0], &tool0)
	metaRaw, ok := tool0["_meta"]
	if !ok {
		t.Fatal("ui_tool: _meta key missing from tools/list response")
	}
	var meta core.ToolMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.UI == nil {
		t.Fatal("ui_tool: _meta.ui is nil")
	}
	if meta.UI.ResourceUri != "ui://myapp/view" {
		t.Errorf("ui_tool: resourceUri = %q, want %q", meta.UI.ResourceUri, "ui://myapp/view")
	}
	if len(meta.UI.Visibility) != 1 || meta.UI.Visibility[0] != core.UIVisibilityModel {
		t.Errorf("ui_tool: visibility = %v, want [model]", meta.UI.Visibility)
	}

	// Second tool should NOT have _meta
	var tool1 map[string]json.RawMessage
	json.Unmarshal(raw.Tools[1], &tool1)
	if _, ok := tool1["_meta"]; ok {
		t.Error("plain_tool: _meta key should be absent")
	}
}
