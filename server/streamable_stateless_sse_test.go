package server

import (
	"bufio"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatelessPostSSE_NotifyFlowsDownPOSTResponse verifies issue #753 —
// a stateless POST that carries Accept: text/event-stream opens the
// response itself as the SSE channel. ctx.Notify() calls during dispatch
// frame as notifications on that stream; the handler's terminal
// *core.Response is the final SSE event.
//
// This is the contract events/stream relies on; the same path works for
// any custom method registered via Server.HandleMethod that emits
// notifications during dispatch.
func TestStatelessPostSSE_NotifyFlowsDownPOSTResponse(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "stateless-sse-test", Version: "0.0.1"})

	// Custom JSON-RPC method that emits 3 notifications then returns
	// a terminal result. Mirrors what events/stream does but without
	// the events package overhead.
	srv.HandleMethod("test/streamemit", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		ctx.Notify("test/notify", map[string]any{"seq": 1, "msg": "first"})
		ctx.Notify("test/notify", map[string]any{"seq": 2, "msg": "second"})
		ctx.Notify("test/notify", map[string]any{"seq": 3, "msg": "third"})
		return core.NewResponse(id, map[string]any{"done": true})
	})

	_, url, teardown := buildStatelessSSEHarness(t, srv, stateless.ModeDual)
	defer teardown()

	// POST with Accept: text/event-stream — should trigger response-as-SSE.
	resp := postStatelessRequestForSSE(t, url, "test/streamemit", map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test-client", "version": "0.0.1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	})
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Content-Type=%q status=%d body=%s", ct, resp.StatusCode, body)
	}

	frames := readSSEPostFrames(t, resp.Body)
	require.GreaterOrEqual(t, len(frames), 4,
		"expected 3 notifications + 1 terminal frame, got %d", len(frames))

	// First three frames must be notifications/test/notify with seq 1/2/3.
	for i, expectedSeq := range []float64{1, 2, 3} {
		var msg map[string]any
		require.NoError(t, json.Unmarshal(frames[i], &msg), "frame %d: %s", i, string(frames[i]))
		assert.Equal(t, "test/notify", msg["method"], "frame %d should be a notification", i)
		params := msg["params"].(map[string]any)
		assert.Equal(t, expectedSeq, params["seq"], "frame %d wrong seq", i)
	}

	// Terminal frame must be the JSON-RPC response with result.
	var terminal map[string]any
	require.NoError(t, json.Unmarshal(frames[3], &terminal),
		"terminal frame: %s", string(frames[3]))
	assert.Nil(t, terminal["method"], "terminal frame must NOT be a notification")
	assert.NotNil(t, terminal["result"], "terminal frame must carry result")
	result := terminal["result"].(map[string]any)
	assert.Equal(t, true, result["done"])
}

// TestStatelessPostSSE_NoAcceptHeaderFallsBackToJSON verifies that when
// the client does NOT request SSE, a stateless POST returns a plain
// application/json response and ctx.Notify silently no-ops (matches
// pre-#753 behavior — no regression for non-streaming clients).
func TestStatelessPostSSE_NoAcceptHeaderFallsBackToJSON(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "stateless-sse-test", Version: "0.0.1"})

	notifyAttempted := false
	srv.HandleMethod("test/streamemit", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		// Notify returns false in this case (no SSE channel attached);
		// handler is expected to fall back to a synchronous return.
		notifyAttempted = ctx.Notify("test/notify", map[string]any{"seq": 1})
		return core.NewResponse(id, map[string]any{"done": true})
	})

	_, url, teardown := buildStatelessSSEHarness(t, srv, stateless.ModeDual)
	defer teardown()

	resp := postStatelessRequestPlain(t, url, "test/streamemit", map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test-client", "version": "0.0.1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	})
	defer resp.Body.Close()

	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.False(t, notifyAttempted,
		"ctx.Notify must return false on stateless POST without Accept: text/event-stream")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var msg map[string]any
	require.NoError(t, json.Unmarshal(body, &msg))
	result := msg["result"].(map[string]any)
	assert.Equal(t, true, result["done"])
}

// TestStatelessPostSSE_HandlerErrorBeforeNotifyFallsBackToJSON verifies
// the lazy-headers safety property — if the handler returns an error
// response before emitting any notification, the transport still has
// the chance to write an HTTP-status-coded application/json response
// rather than committing to text/event-stream prematurely. Matches the
// legacy handlePostSSE behavior.
func TestStatelessPostSSE_HandlerErrorBeforeNotifyFallsBackToJSON(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "stateless-sse-test", Version: "0.0.1"})

	srv.HandleMethod("test/streamemit", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "no good")
	})

	_, url, teardown := buildStatelessSSEHarness(t, srv, stateless.ModeDual)
	defer teardown()

	resp := postStatelessRequestForSSE(t, url, "test/streamemit", map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test-client", "version": "0.0.1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	})
	defer resp.Body.Close()

	// HTTP 400 from stateless.HTTPStatusForCode for -32602 + JSON body.
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"InvalidParams should surface as 400")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"),
		"handler error before any notify should NOT commit to SSE headers")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var msg map[string]any
	require.NoError(t, json.Unmarshal(body, &msg))
	errObj := msg["error"].(map[string]any)
	assert.Equal(t, float64(core.ErrCodeInvalidParams), errObj["code"])
	assert.Equal(t, "no good", errObj["message"])
}

// --- helpers ------------------------------------------------------

func buildStatelessSSEHarness(t *testing.T, srv *Server, mode stateless.Mode) (*Server, string, func()) {
	t.Helper()
	handler := srv.Handler(WithStreamableHTTP(true), WithStatelessMode(mode))
	ts := httptest.NewServer(handler)
	return srv, ts.URL + "/mcp", ts.Close
}

// postStatelessRequestForSSE sends a JSON-RPC POST with the SSE-eligible
// Accept header and returns the raw HTTP response so the caller can
// read the SSE-framed body line-by-line.
func postStatelessRequestForSSE(t *testing.T, url, method string, params any) *http.Response {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set(mcpProtocolVersionHeader, "2026-07-28")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// postStatelessRequestPlain mirrors postStatelessRequestForSSE without
// the SSE-eligible Accept header — used to verify the non-SSE path
// regresses cleanly.
func postStatelessRequestPlain(t *testing.T, url, method string, params any) *http.Response {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(mcpProtocolVersionHeader, "2026-07-28")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// readSSEPostFrames parses the SSE body into a slice of raw JSON payloads
// (one per `data:` line). Stops at EOF.
func readSSEPostFrames(t *testing.T, r io.Reader) [][]byte {
	t.Helper()
	var frames [][]byte
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		frames = append(frames, []byte(strings.TrimSpace(line[len("data:"):])))
	}
	return frames
}
