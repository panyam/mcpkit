package events

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStream_Stateless_ReturnsUnsupported asserts that an events/stream
// call arriving on the SEP-2575 stateless wire is refused with the
// spec-shaped -32014 Unsupported response — data.feature="deliveryMode",
// data.value="push" — rather than running a heartbeat loop whose
// ctx.Notify calls silently no-op (the stateless wire has no per-
// request notify channel).
//
// Pairs with the CanNotify guard added at the top of registerStream.
// Same data shape the source-lacks-push branch already uses, so the
// client picks events/poll via one decision rule regardless of which
// rail is missing.
func TestStream_Stateless_ReturnsUnsupported(t *testing.T) {
	src, _ := NewYieldingSource[fakePayload](
		EventDef{Name: "fake.event", Description: "test", Delivery: []string{"push", "poll"}},
	)
	srv := server.NewServer(core.ServerInfo{Name: "stateless-events-test", Version: "0.0.1"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-anon", // bypass auth gate; the assertion is on the wire-level branch
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
