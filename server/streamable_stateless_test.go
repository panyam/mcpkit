package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// newStatelessTestServer spins up a Streamable HTTP server with one
// tool registered + the requested wire mode. Returns the test server,
// its URL, and a teardown closure.
func newStatelessTestServer(t *testing.T, mode stateless.Mode) (*Server, string, func()) {
	t.Helper()
	s := NewServer(core.ServerInfo{Name: "stateless-test", Version: "0.0.1"})
	// Register a trivial tool so tools/list returns something.
	if err := s.Registry().AddTool(
		core.ToolDef{Name: "echo", Description: "echoes back"},
		func(_ core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: string(req.Arguments)}}}, nil
		},
	); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	// And a tool that triggers the -32003 path so we can exercise
	// translateToolError end-to-end.
	if err := s.Registry().AddTool(
		core.ToolDef{Name: "test_missing_capability"},
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.ToolResult{}, &core.MissingCapabilityError{
				Required: core.ClientCapabilities{Sampling: &struct{}{}},
				Message:  "this tool requires sampling",
			}
		},
	); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	handler := s.Handler(WithStreamableHTTP(true), WithStatelessMode(mode))
	ts := httptest.NewServer(handler)
	return s, ts.URL + "/mcp", func() { ts.Close() }
}

// postStatelessJSON sends a JSON-RPC POST to the test server and returns
// the HTTP response object (status code + body still readable).
func postStatelessJSON(t *testing.T, url string, body any, headers map[string]string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", core.StreamableHTTPAccept)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

// decode reads body into a core.Response (id+result+error envelope).
// Tolerant of both plain JSON (stateless wire) and SSE-framed
// (`data: {...}`) responses that the legacy transport emits when
// the request opted into text/event-stream.
func decode(t *testing.T, resp *http.Response) *core.Response {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	defer resp.Body.Close()
	payload := bytes.TrimSpace(body)

	// SSE frame: skip past id:/event: lines, take the JSON after data:.
	if idx := bytes.Index(payload, []byte("data:")); idx >= 0 {
		payload = bytes.TrimSpace(payload[idx+len("data:"):])
	}

	var out core.Response
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("decode body (%d bytes): %v\n%s", len(body), err, string(body))
	}
	return &out
}

const draftVersion = core.DraftProtocolVersion2026V1

func validMetaParams() map[string]any {
	return map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    draftVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}
}

// TestStatelessRouting_DualMode_ServerDiscoverHits200 verifies that a
// server/discover request lands on the stateless dispatcher under
// ModeDual and returns the expected {supportedVersions, ...} shape.
func TestStatelessRouting_DualMode_ServerDiscoverHits200(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "server/discover",
		"params": validMetaParams(),
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, body=%s", resp.StatusCode, string(body))
	}
	r := decode(t, resp)
	if r.Error != nil {
		t.Fatalf("error: %+v", r.Error)
	}
	// Result is a map after unmarshal; verify the shape.
	raw, _ := json.Marshal(r.Result)
	var dr stateless.DiscoverResult
	if err := json.Unmarshal(raw, &dr); err != nil {
		t.Fatalf("decode DiscoverResult: %v", err)
	}
	if len(dr.SupportedVersions) == 0 || dr.SupportedVersions[0] != draftVersion {
		t.Errorf("SupportedVersions = %v, want first=%q", dr.SupportedVersions, draftVersion)
	}
	if dr.ServerInfo.Name != "stateless-test" {
		t.Errorf("ServerInfo.Name = %q, want stateless-test", dr.ServerInfo.Name)
	}
}

// TestStatelessRouting_DualMode_RemovedMethodReturns404 verifies the
// SEP-2575 HTTP status mapping: -32601 from the stateless dispatcher
// surfaces as HTTP 404, distinguishing it from "happen to send the
// legacy path" (which returns 200 with body errors).
func TestStatelessRouting_DualMode_RemovedMethodReturns404(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	// Force the stateless path via the body _meta envelope even though
	// the method itself is "ping" (which is legacy-only). Signal 3
	// (_meta.protocolVersion present) routes to stateless; the
	// dispatcher then -32601s because "ping" isn't a stateless method.
	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "ping",
		"params": validMetaParams(),
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})
	if resp.StatusCode != 404 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status %d, want 404; body=%s", resp.StatusCode, string(body))
	}
	r := decode(t, resp)
	if r.Error == nil || r.Error.Code != core.ErrCodeMethodNotFound {
		t.Errorf("expected -32601, got %+v", r.Error)
	}
}

// TestStatelessRouting_DualMode_HeaderMismatchReturns400 verifies the
// -32001 + HTTP 400 path when MCP-Protocol-Version header and the _meta
// protocolVersion field disagree.
func TestStatelessRouting_DualMode_HeaderMismatchReturns400(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	}, map[string]string{mcpProtocolVersionHeader: "1900-01-01"})
	if resp.StatusCode != 400 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status %d, want 400; body=%s", resp.StatusCode, string(body))
	}
	r := decode(t, resp)
	if r.Error == nil || r.Error.Code != core.ErrCodeHeaderMismatch {
		t.Errorf("expected -32001, got %+v", r.Error)
	}
}

// TestStatelessRouting_DualMode_LegacyInitializeStillWorks is the
// load-bearing dual-mode test: a legacy initialize request against
// the same URL still hits the legacy dispatcher and gets a session.
// Proves Dual mode is genuinely additive — pre-2575 clients keep
// working transparently.
func TestStatelessRouting_DualMode_LegacyInitializeStillWorks(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"clientInfo":      map[string]any{"name": "legacy", "version": "1"},
			"capabilities":    map[string]any{},
		},
	}, nil)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("legacy initialize got %d, want 200; body=%s",
			resp.StatusCode, string(body))
	}
	if resp.Header.Get(mcpSessionIDHeader) == "" {
		t.Errorf("legacy initialize did not return Mcp-Session-Id header")
	}
	r := decode(t, resp)
	if r.Error != nil {
		t.Errorf("legacy initialize error: %+v", r.Error)
	}
}

// TestStatelessRouting_LegacyOnly_DiscoverReturns404 verifies that
// ModeLegacyOnly refuses server/discover. Since the stateless
// dispatcher is nil under this mode, the request falls through to
// the legacy path — which doesn't know server/discover and returns
// -32601 in the body but with HTTP 200 (legacy semantics).
func TestStatelessRouting_LegacyOnly_DiscoverFallsThrough(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeLegacyOnly)
	defer teardown()

	// Need a session to talk to legacy. Initialize first.
	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"clientInfo":      map[string]any{"name": "t", "version": "1"},
			"capabilities":    map[string]any{},
		},
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("initialize: %d", resp.StatusCode)
	}
	sid := resp.Header.Get(mcpSessionIDHeader)
	resp.Body.Close()

	// Complete the legacy lifecycle so the session is fully initialized
	// (notifications/initialized) — otherwise subsequent calls return
	// -32600 "not initialized", which would mask the routing test.
	notify := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	}, map[string]string{mcpSessionIDHeader: sid})
	notify.Body.Close()

	// Now ask for server/discover — legacy path doesn't know it.
	resp = postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "server/discover",
		"params": validMetaParams(),
	}, map[string]string{mcpSessionIDHeader: sid})
	// Legacy semantics: HTTP 200, body carries -32601.
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status %d, want 200 (legacy semantics); body=%s",
			resp.StatusCode, string(body))
	}
	r := decode(t, resp)
	if r.Error == nil || r.Error.Code != core.ErrCodeMethodNotFound {
		t.Errorf("expected -32601, got %+v", r.Error)
	}
}

// TestStatelessRouting_DualMode_ToolsCallMissingCap404 verifies the
// end-to-end -32003 + HTTP 400 path: a tool handler returns
// *core.MissingCapabilityError, the dispatcher translates it, and
// the transport stamps the right HTTP status.
func TestStatelessRouting_DualMode_ToolsCallMissingCap(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	resp := postStatelessJSON(t, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "test_missing_capability",
			"arguments": map[string]any{},
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	}, map[string]string{mcpProtocolVersionHeader: draftVersion})

	if resp.StatusCode != 400 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status %d, want 400; body=%s", resp.StatusCode, string(body))
	}
	r := decode(t, resp)
	if r.Error == nil || r.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Errorf("expected -32003, got %+v", r.Error)
	}
}

// TestStatelessRouting_DetectionPriorities verifies the precedence
// table the detect helper documents. Lower-numbered signals must
// win even when conflicting higher-numbered signals are present.
func TestStatelessRouting_DetectionPriorities(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		headers  map[string]string
		paramsOK bool // include valid _meta envelope
		want     wireKind
	}{
		{"initialize → legacy regardless of stateless header",
			"initialize", map[string]string{mcpProtocolVersionHeader: draftVersion}, true, wireLegacy},
		{"server/discover → stateless", "server/discover", nil, true, wireStateless},
		{"header alone is not a wire signal (universal post-init header)",
			"tools/list", map[string]string{mcpProtocolVersionHeader: draftVersion}, false, wireLegacy},
		{"_meta-only → stateless", "tools/list", nil, true, wireStateless},
		{"session-id only → legacy", "tools/list",
			map[string]string{mcpSessionIDHeader: "abc"}, false, wireLegacy},
		{"session-id + header → legacy (universal header doesn't override session)",
			"tools/list", map[string]string{
				mcpSessionIDHeader:       "abc",
				mcpProtocolVersionHeader: draftVersion,
			}, false, wireLegacy},
		{"no signals → legacy default", "tools/list", nil, false, wireLegacy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(""))
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			var params json.RawMessage
			if c.paramsOK {
				p, _ := json.Marshal(validMetaParams())
				params = p
			}
			req := &core.Request{Method: c.method, Params: params}
			got := detectWireKind(r, nil, req, stateless.ModeDual)
			if got != c.want {
				t.Errorf("detectWireKind = %v, want %v", got, c.want)
			}
		})
	}
}
