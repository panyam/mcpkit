package events

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
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStream_Stateless_SSE_DeliversEvents asserts that an events/stream
// call arriving on the SEP-2575 stateless wire with Accept:
// text/event-stream opens the response itself as the push channel:
// the active frame, every yielded event, and heartbeats arrive as SSE
// notification frames on the open POST response. Validates that PR 754
// (stateless POST-as-SSE) plus core.BaseContext.CanNotify wire all the
// way through to a real events/stream subscription.
//
// This is the contract the whole-enchilada streamer demo binary relies
// on. Without the CanNotify-gated admission path, events/stream would
// either reject with -32014 (old guard) or accept and silently drop
// events (no guard at all).
func TestStream_Stateless_SSE_DeliversEvents(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](
		EventDef{Name: "fake.event", Description: "test", Delivery: []string{"push", "poll"}},
	)
	srv := server.NewServer(core.ServerInfo{Name: "stateless-events-test", Version: "0.0.1"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-anon",
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeStateless))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "events/stream",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    core.DraftProtocolVersion2026V1,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
			"name": "fake.event",
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), "POST", ts.URL+"/mcp", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", core.StreamableHTTPAccept)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"events/stream over stateless+SSE must commit to SSE headers — admission via CanNotify should have passed")

	// Yield an event server-side once the subscription is active. The
	// dispatcher routes us through the streamer's loop, so the simple
	// way to drive it is to push from a goroutine while the test
	// blocks on the SSE reader.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = yield(context.Background(), fakePayload{Msg: "hi"})
		// Give the server-side fanout a moment to flush before closing the connection.
		time.Sleep(200 * time.Millisecond)
		// Force-close the response so the SSE reader returns.
		resp.Body.Close()
	}()

	frames := readSSEFrames(t, resp.Body)
	require.GreaterOrEqual(t, len(frames), 1,
		"expected at least one SSE frame (active marker); got %d", len(frames))

	// The first frame must be a notifications/events/active marker
	// (events/stream announces the subscription before sending data).
	var first map[string]any
	require.NoError(t, json.Unmarshal(frames[0], &first), "frame 0: %s", string(frames[0]))
	method, _ := first["method"].(string)
	assert.True(t, strings.HasPrefix(method, "notifications/events/"),
		"first frame should be a notifications/events/* notification, got %q (%s)",
		method, string(frames[0]))

	// Look for the yielded event in any of the frames.
	sawEvent := false
	for _, frame := range frames {
		var msg map[string]any
		if err := json.Unmarshal(frame, &msg); err != nil {
			continue
		}
		if m, _ := msg["method"].(string); m == "notifications/events/event" {
			sawEvent = true
			break
		}
	}
	assert.True(t, sawEvent, "expected at least one notifications/events/event frame; got frames=%d", len(frames))
}

// TestStream_Stateless_PlainJSON_ReturnsUnsupported asserts the
// admission-gate complement — a stateless POST that does NOT request
// SSE has no notify channel. CanNotify returns false and registerStream
// fails fast with the spec-shape -32014 Unsupported response. Clients
// pick events/poll via one decision rule regardless of whether the rail
// is missing because the source lacks push or because the wire does.
func TestStream_Stateless_PlainJSON_ReturnsUnsupported(t *testing.T) {
	src, _ := NewYieldingSource[fakePayload](
		EventDef{Name: "fake.event", Description: "test", Delivery: []string{"push", "poll"}},
	)
	srv := server.NewServer(core.ServerInfo{Name: "stateless-events-test", Version: "0.0.1"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-anon",
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeStateless))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "events/stream",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    core.DraftProtocolVersion2026V1,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
			"name": "fake.event",
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), "POST", ts.URL+"/mcp", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	// JSON-only Accept — NO text/event-stream. Stateless transport
	// returns single-shot JSON and never attaches a NotifyFunc.
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)

	var rpc core.Response
	require.NoError(t, json.Unmarshal(payload, &rpc), "body: %s", string(payload))
	require.NotNil(t, rpc.Error, "expected -32014; got result: %s", string(payload))
	assert.Equal(t, ErrCodeUnsupported, rpc.Error.Code, "want -32014 Unsupported")

	dataRaw, err := json.Marshal(rpc.Error.Data)
	require.NoError(t, err)
	var data UnsupportedData
	require.NoError(t, json.Unmarshal(dataRaw, &data))
	assert.Equal(t, "deliveryMode", data.Feature)
	assert.Equal(t, "push", data.Value)
}

// readSSEFrames pulls the JSON payloads off `data:` lines of an SSE
// response body until EOF / read error.
func readSSEFrames(t *testing.T, r io.Reader) [][]byte {
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
