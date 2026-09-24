package main

import (
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newConformanceTestClient(t *testing.T) *client.Client {
	t.Helper()
	w := buildServer(":0", nil, true)
	ts := httptest.NewServer(w.srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return newTestClient(t, ts)
}

func conformanceSubscribe(t *testing.T, c *client.Client, url string) error {
	t.Helper()
	_, err := c.Call(t.Context(), "events/subscribe", map[string]any{
		"name":     "alert.fired",
		"delivery": map[string]any{"mode": "webhook", "url": url, "secret": events.GenerateSecret()},
	})
	return err
}

// The events-webhook scenario subscribes to callbacks under a placeholder
// origin that never resolves, because it grades subscribe semantics rather
// than delivery. Endpoint verification refuses those, so under
// --conformance-events the fixture allowlists that one origin, spec path (b).
func TestConformanceEvents_SuiteCallbackOriginIsAllowlisted(t *testing.T) {
	c := newConformanceTestClient(t)
	require.NoError(t, conformanceSubscribe(t, c, "https://conformance.invalid/mcp-events/1790000000"))
}

func TestConformanceEvents_OtherUnreachableCallbacksStillVerified(t *testing.T) {
	c := newConformanceTestClient(t)
	err := conformanceSubscribe(t, c, "https://elsewhere.invalid/mcp-events/1")
	require.Error(t, err)
	rpc := unwrapRPC(err)
	require.NotNil(t, rpc, "want an RPC error, got %v", err)
	assert.Equal(t, events.ErrCodeCallbackEndpointError, rpc.Code)
}
