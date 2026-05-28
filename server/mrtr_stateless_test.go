package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// SEP-2322 MRTR over the SEP-2575 stateless wire.
//
// The conformance scenarios in `input-required-result-*` POST tools/call with
// no prior initialize handshake, carrying `_meta.{protocolVersion,clientInfo,
// clientCapabilities}` per-request and (on retry) `inputResponses` + the
// echoed `requestState`. The legacy `Dispatcher.handleToolsCall` decodes the
// MRTR envelope and reshapes `InputRequiredResult` responses; the stateless
// `callToolForStateless` path must do the same.
//
// Each test sets up a single tool that emits an InputRequiredResult on the
// first round and a complete ToolResult on the second, then drives both
// rounds via raw HTTP and asserts the wire fields.

// newStatelessMRTRServer registers a tool that round-trips a single
// elicitation input, optionally with WithRequestStateSigning enabled.
func newStatelessMRTRServer(t *testing.T, opts ...Option) (*Server, string, func()) {
	t.Helper()
	s := NewServer(core.ServerInfo{Name: "stateless-mrtr-test", Version: "0.0.1"}, opts...)

	if err := s.Registry().AddTool(
		core.ToolDef{
			Name:        "test_input_required_result_elicitation",
			Description: "Asks for the user's name then greets them.",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}}}}`),
					},
				})
			}
			raw := ctx.InputResponse("user_name")
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Name string `json:"name"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &er); err != nil {
				return core.ErrorResult("malformed elicitation response"), nil
			}
			return core.TextResult("Hello, " + er.Content.Name + "!"), nil
		},
	); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	handler := s.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeDual))
	ts := httptest.NewServer(handler)
	return s, ts.URL + "/mcp", func() { ts.Close() }
}

// statelessToolsCall is a tiny convenience for tools/call requests carrying
// the SEP-2575 _meta envelope. extra is merged into the params top-level so
// callers can add inputResponses, requestState, arguments, etc.
//
// Returns the decoded JSON-RPC response regardless of HTTP status — a
// structured -32602 / -32003 maps to HTTP 4xx, and callers want to inspect
// the JSON-RPC error rather than fail at the transport layer.
func statelessToolsCall(t *testing.T, url, toolName string, extra map[string]any) *core.Response {
	t.Helper()
	params := map[string]any{
		"name":      toolName,
		"arguments": map[string]any{},
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    draftVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{"elicitation": map[string]any{}, "sampling": map[string]any{}, "roots": map[string]any{}},
		},
	}
	for k, v := range extra {
		params[k] = v
	}
	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": params,
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})
	return decode(t, resp)
}

// TestStatelessMRTR_RoundTripBasicElicitation drives the A1 conformance
// scenario shape over the stateless wire: round 1 returns
// InputRequiredResult with non-empty requestState; round 2 (same tool, with
// inputResponses + the echoed requestState) returns a complete ToolResult.
func TestStatelessMRTR_RoundTripBasicElicitation(t *testing.T) {
	_, url, teardown := newStatelessMRTRServer(t)
	defer teardown()

	r1 := statelessToolsCall(t, url, "test_input_required_result_elicitation", nil)
	if r1.Error != nil {
		t.Fatalf("round 1 error: %+v", r1.Error)
	}
	m1, _ := r1.Result.(map[string]any)
	if m1 == nil {
		t.Fatalf("round 1 result not a map: %T %+v", r1.Result, r1.Result)
	}
	if got := m1["resultType"]; got != "input_required" {
		t.Fatalf("round 1 resultType = %v, want \"input_required\"", got)
	}
	reqs, ok := m1["inputRequests"].(map[string]any)
	if !ok {
		t.Fatalf("round 1 inputRequests missing")
	}
	if _, ok := reqs["user_name"]; !ok {
		t.Fatalf("round 1 inputRequests[user_name] missing; got %v", reqs)
	}
	state1, _ := m1["requestState"].(string)
	if state1 == "" {
		t.Fatalf("round 1 requestState empty")
	}

	r2 := statelessToolsCall(t, url, "test_input_required_result_elicitation", map[string]any{
		"inputResponses": map[string]any{
			"user_name": map[string]any{"action": "accept", "content": map[string]any{"name": "Alice"}},
		},
		"requestState": state1,
	})
	if r2.Error != nil {
		t.Fatalf("round 2 error: %+v", r2.Error)
	}
	m2, _ := r2.Result.(map[string]any)
	if m2 == nil {
		t.Fatalf("round 2 result not a map")
	}
	if m2["resultType"] == "input_required" {
		t.Fatalf("round 2 still input_required; expected complete")
	}
	content, _ := m2["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("round 2 content empty: %v", m2)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "Alice") {
		t.Errorf("round 2 text = %q, want substring \"Alice\"", text)
	}
}

// TestStatelessMRTR_TamperedRequestStateRejected exercises the A12
// scenario shape: a signed requestState that's been mutated MUST yield a
// JSON-RPC error rather than a complete result or a fresh
// InputRequiredResult.
func TestStatelessMRTR_TamperedRequestStateRejected(t *testing.T) {
	_, url, teardown := newStatelessMRTRServer(t, WithRequestStateSigning([]byte("test-key-32bytes-for-hmacsha256-x"), 0))
	defer teardown()

	r1 := statelessToolsCall(t, url, "test_input_required_result_elicitation", nil)
	if r1.Error != nil {
		t.Fatalf("round 1 error: %+v", r1.Error)
	}
	m1, _ := r1.Result.(map[string]any)
	state1, _ := m1["requestState"].(string)
	if state1 == "" {
		t.Fatalf("round 1 requestState empty under WithRequestStateSigning")
	}

	r2 := statelessToolsCall(t, url, "test_input_required_result_elicitation", map[string]any{
		"inputResponses": map[string]any{
			"user_name": map[string]any{"action": "accept", "content": map[string]any{"name": "Alice"}},
		},
		"requestState": state1 + "-TAMPERED",
	})
	if r2.Error == nil {
		t.Fatalf("expected JSON-RPC error for tampered requestState; got result %v", r2.Result)
	}
}
