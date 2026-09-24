package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
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

func subscribeForID(t *testing.T, c *client.Client, url string, ttlMs any) (id string, refreshBefore any) {
	t.Helper()
	raw, err := c.Call(t.Context(), "events/subscribe", map[string]any{
		"name":     "alert.fired",
		"delivery": map[string]any{"mode": "webhook", "url": url, "secret": events.GenerateSecret()},
		"ttlMs":    ttlMs,
	})
	require.NoError(t, err)
	var res map[string]any
	require.NoError(t, json.Unmarshal(raw.Raw, &res))
	id, _ = res["id"].(string)
	require.NotEmpty(t, id)
	return id, res["refreshBefore"]
}

func subscriptionState(t *testing.T, c *client.Client, id string) string {
	t.Helper()
	text, err := c.ToolCall(t.Context(), "events_conformance_subscription_state", map[string]any{"id": id})
	require.NoError(t, err)
	return text
}

// The TTL rows need a restart the harness can ask for. The control rebuilds
// the server and registry over the same store; the old session has to die
// (proof the rebuild happened) and both grants have to survive it.
func TestConformanceEvents_RestartKeepsLongAndNoExpiryGrants(t *testing.T) {
	cs := newConformanceServer(":0", nil, nil)
	ts := httptest.NewServer(cs)
	t.Cleanup(ts.Close)

	before := newTestClient(t, ts)
	noExpiry, rb := subscribeForID(t, before, "https://conformance.invalid/mcp-events/ttl-null", nil)
	assert.Nil(t, rb, "the fixture opts into no-expiry grants, so ttlMs:null is granted as refreshBefore:null")
	long, rb := subscribeForID(t, before, "https://conformance.invalid/mcp-events/ttl-long", 24*3600*1000)
	assert.NotNil(t, rb)

	target, err := before.ToolCall(t.Context(), "events_conformance_restart", map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "2", target, "the restart control answers the generation it is moving to")
	require.Eventually(t, func() bool {
		_, err := before.ToolCall(t.Context(), "events_conformance_subscription_state", map[string]any{"id": noExpiry})
		return err != nil
	}, 3*time.Second, 50*time.Millisecond, "the pre-restart session must not survive the restart")

	after := newTestClient(t, ts)
	gen, err := after.ToolCall(t.Context(), "events_conformance_generation", map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, target, gen)
	assert.Equal(t, "active", subscriptionState(t, after, noExpiry))
	assert.Equal(t, "active", subscriptionState(t, after, long))
	assert.Equal(t, "absent", subscriptionState(t, after, "sub_never_existed"))
}

// Plain --serve lifts the routability guard for `make demo`; the conformance
// build must not, or the SSRF rows pass on the scheme rule alone.
func TestConformanceEvents_RefusesNonRoutableHTTPSCallback(t *testing.T) {
	c := newConformanceTestClient(t)
	err := conformanceSubscribe(t, c, "https://127.0.0.1:1/mcp-events")
	require.Error(t, err)
	rpc := unwrapRPC(err)
	require.NotNil(t, rpc, "want an RPC error, got %v", err)
	assert.Equal(t, core.ErrCodeInvalidParams, rpc.Code)
}

func newVerifyingReceiver(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if !events.AnswerVerificationChallenge(w, body) {
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestConformanceEvents_AllowCallbackOriginPermitsThatOriginOnly(t *testing.T) {
	c := newConformanceTestClient(t)
	receiver := newVerifyingReceiver(t)
	other := newVerifyingReceiver(t)

	require.Error(t, conformanceSubscribe(t, c, receiver.URL+"/mcp-events"), "refused before the control")

	res, err := c.ToolCallFull(t.Context(), "events_conformance_allow_callback_origin",
		map[string]any{"origin": receiver.URL})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, receiver.URL, res.Content[0].Text)

	require.NoError(t, conformanceSubscribe(t, c, receiver.URL+"/mcp-events"))
	assert.Error(t, conformanceSubscribe(t, c, other.URL+"/mcp-events"), "another origin stays refused")
}

func TestConformanceEvents_AllowCallbackOriginRejectsNonOrigin(t *testing.T) {
	c := newConformanceTestClient(t)
	res, err := c.ToolCallFull(t.Context(), "events_conformance_allow_callback_origin",
		map[string]any{"origin": "http://127.0.0.1:8080/path"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
