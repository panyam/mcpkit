package eventsclient_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEP-3415 § Streamable HTTP: Routing Headers, end to end on both wires: the
// client stamps Mcp-Name on events/poll (Base64-sentinel for a non-ASCII
// name), the server decodes and accepts it, and a raw events/poll without the
// header is refused with 400 + -32020.
func TestRoutingHeaders_EventsPollCarriesMcpName(t *testing.T) {
	const name = "天気.changed"
	for _, mode := range []client.ClientMode{client.ClientModeStateless, client.ClientModeLegacyOnly} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			src, _ := events.NewYieldingSource[fakePayload](events.EventDef{Name: name, Description: "test", Delivery: []string{"poll"}})
			srv := server.NewServer(core.ServerInfo{Name: "routing-headers", Version: "0.1.0"})
			events.Register(events.Config{
				Sources:                  []events.EventSource{src},
				Webhooks:                 events.NewWebhookRegistry(),
				Server:                   srv,
				UnsafeAnonymousPrincipal: "test-anon",
			})
			var mu sync.Mutex
			seen := map[string]string{}
			inner := srv.Handler(server.WithStreamableHTTP(true))
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				if m := r.Header.Get(core.McpMethodHeader); m != "" {
					seen[m] = r.Header.Get(core.McpNameHeader)
				}
				mu.Unlock()
				inner.ServeHTTP(w, r)
			}))
			t.Cleanup(ts.Close)

			c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, client.WithClientMode(mode))
			require.NoError(t, c.Connect(t.Context()))
			t.Cleanup(func() { c.Close() })

			p, err := eventsclient.Poll(t.Context(), c, eventsclient.PollOptions{EventName: name})
			require.NoError(t, err)
			p.Stop()
			<-p.Done()

			mu.Lock()
			got, ok := seen["events/poll"]
			mu.Unlock()
			require.True(t, ok, "no events/poll request reached the server")
			want, _ := core.EncodeMcpHeaderValue(name)
			assert.Equal(t, want, got)
			assert.Contains(t, got, "=?base64?", "a non-ASCII event name goes in the sentinel form")
		})
	}
}

func TestRoutingHeaders_StatelessEventsPollWithoutMcpNameIsRefused(t *testing.T) {
	src, _ := events.NewYieldingSource[fakePayload](events.EventDef{Name: "fake.event", Description: "test", Delivery: []string{"poll"}})
	srv := server.NewServer(core.ServerInfo{Name: "routing-headers", Version: "0.1.0"})
	events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Webhooks:                 events.NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-anon",
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "events/poll",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    core.DraftProtocolVersion2026V1,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
			"name": "fake.event",
		},
	})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), "POST", ts.URL+"/mcp", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", core.StreamableHTTPAccept)
	req.Header.Set(core.McpMethodHeader, "events/poll")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body struct {
		Error *core.Error `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotNil(t, body.Error)
	assert.Equal(t, core.ErrCodeHeaderMismatch, body.Error.Code)
}
