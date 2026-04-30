package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestStack constructs a server + source + yield closure used by all
// e2e tests. The source and yield are returned so tests can directly publish
// events without spinning up a Discord session. Optional WebhookOptions
// configure the registry — defaults match the production demo (Server +
// MCPHeaders).
func buildTestStack(whOpts ...events.WebhookOption) (*server.Server, *events.YieldingSource[DiscordEventData], func(DiscordEventData) error, *events.WebhookRegistry) {
	webhooks := events.NewWebhookRegistry(whOpts...)
	source, yield := newDiscordSource()

	srv := server.NewServer(
		core.ServerInfo{Name: "discord-events-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, source)
	registerTools(srv, nil) // nil session = test mode
	events.Register(events.Config{
		Sources:  []events.EventSource{source},
		Webhooks: webhooks,
		Server:   srv,
	})

	return srv, source, yield, webhooks
}

func connectClient(t *testing.T, srv *server.Server) (*client.Client, *httptest.Server) {
	t.Helper()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	return c, ts
}

// pollResult mirrors the events/poll per-subscription result.
type pollResult struct {
	ID      string         `json:"id"`
	Events  []events.Event `json:"events"`
	Cursor  string         `json:"cursor"`
	HasMore bool           `json:"hasMore"`
}

// TestE2EPollDelivery verifies events/poll returns events that were yielded
// via the YieldingSource path. Confirms the library's internal Poll handler
// reads through the user-provided EventStore correctly.
func TestE2EPollDelivery(t *testing.T) {
	srv, _, yield, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	require.NoError(t, yield(newDiscordEvent("guild-1", "channel-1", "alice", "hello", time.Now())))
	require.NoError(t, yield(newDiscordEvent("guild-1", "channel-1", "bob", "world", time.Now())))

	result, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "poll", "name": "discord.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Results, 1)
	assert.Len(t, resp.Results[0].Events, 2)

	var data DiscordEventData
	require.NoError(t, json.Unmarshal(resp.Results[0].Events[0].Data, &data))
	assert.Equal(t, "alice", data.Author.Username)
	assert.Equal(t, "hello", data.Content)
	assert.Equal(t, "guild-1", data.GuildID)
	assert.Equal(t, "channel-1", data.ChannelID)
}

// TestE2EPushDelivery verifies the library's automatic push fanout — yield()
// triggers events.Emit via the SetEmitHook wired by Register, and the SSE
// notification reaches the client. Source author writes no fanout code.
func TestE2EPushDelivery(t *testing.T) {
	srv, _, yield, _ := buildTestStack()

	var mu sync.Mutex
	var received []string

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "push-test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithNotificationCallback(func(method string, params any) {
			mu.Lock()
			received = append(received, method)
			mu.Unlock()
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	time.Sleep(200 * time.Millisecond)
	require.NoError(t, yield(newDiscordEvent("g", "c", "alice", "push test", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, received, "notifications/events/event")
}

// TestE2EWebhookDelivery verifies webhook fanout via the default secret mode
// (Server). Demonstrates the post-PR-C contract: the server generates the
// secret regardless of what the client supplied, returns it in the subscribe
// response, and signs deliveries with the generated secret. The receiver
// uses the returned secret to verify.
func TestE2EWebhookDelivery(t *testing.T) {
	srv, _, yield, webhooks := buildTestStack()

	var mu sync.Mutex
	var deliveries []events.Event
	var assignedSecret string

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-MCP-Signature")
		ts := r.Header.Get("X-MCP-Timestamp")
		mu.Lock()
		secret := assignedSecret
		mu.Unlock()
		assert.True(t, events.VerifyMCPSignature(body, secret, ts, sig), "signature should verify against the server-assigned secret")

		var event events.Event
		json.Unmarshal(body, &event)
		mu.Lock()
		deliveries = append(deliveries, event)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	c, _ := connectClient(t, srv)

	subResult, err := c.Call("events/subscribe", map[string]any{
		"id":       "wh",
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": "ignored-in-server-mode"},
	})
	require.NoError(t, err)
	require.Len(t, webhooks.Targets(), 1)

	var subResp struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(subResult.Raw, &subResp))
	require.NotEmpty(t, subResp.Secret, "server mode must return its generated secret")
	require.NotEqual(t, "ignored-in-server-mode", subResp.Secret, "server mode must NOT echo the client-supplied secret")
	mu.Lock()
	assignedSecret = subResp.Secret
	mu.Unlock()

	require.NoError(t, yield(newDiscordEvent("g", "c", "bob", "webhook test", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1)
	assert.Equal(t, "discord.message", deliveries[0].Name)
}

// TestE2EEventsList verifies events/list returns the discord.message
// definition with payloadSchema auto-derived from DiscordEventData (nested
// objects, optional fields, enums). Confirms YieldingSource preserves the
// schema-derivation ergonomic from TypedSource.
func TestE2EEventsList(t *testing.T) {
	srv, _, _, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	result, err := c.Call("events/list", map[string]any{})
	require.NoError(t, err)

	var resp struct {
		Events []struct {
			Name          string   `json:"name"`
			Delivery      []string `json:"delivery"`
			PayloadSchema any      `json:"payloadSchema"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Events, 1)
	assert.Equal(t, "discord.message", resp.Events[0].Name)
	assert.ElementsMatch(t, []string{"push", "poll", "webhook"}, resp.Events[0].Delivery)
	assert.NotNil(t, resp.Events[0].PayloadSchema, "payloadSchema should be auto-derived")
}

// TestE2EResourceRead verifies the recent-messages resource reads from the
// same store the events/poll path uses — single source of truth, no separate
// message buffer. Round-trips a yielded event through the resource read path.
func TestE2EResourceRead(t *testing.T) {
	srv, _, yield, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	require.NoError(t, yield(newDiscordEvent("g", "c", "alice", "resource test", time.Now())))

	text, err := c.ReadResource("discord://messages/recent")
	require.NoError(t, err)
	assert.Contains(t, text, "resource test")

	var payloads []DiscordEventData
	require.NoError(t, json.Unmarshal([]byte(text), &payloads))
	require.Len(t, payloads, 1)
	assert.Equal(t, "alice", payloads[0].Author.Username)
	assert.Equal(t, "resource test", payloads[0].Content)
}

// TestE2EWebhookDelivery_IdentityMode verifies the identity-mode contract
// end-to-end: subscribe is idempotent on the (name, url, params) tuple, the
// server returns derived id and secret, and webhook delivery signs with
// the derived secret. Two subscribes with the same tuple must collapse to
// one registry entry.
func TestE2EWebhookDelivery_IdentityMode(t *testing.T) {
	srv, _, yield, webhooks := buildTestStack(
		events.WithWebhookSecretMode(events.WebhookSecretIdentity),
		events.WithWebhookRoot([]byte("test-root-master")),
	)

	var mu sync.Mutex
	var deliveries []events.Event
	var assignedSecret string

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-MCP-Signature")
		ts := r.Header.Get("X-MCP-Timestamp")
		mu.Lock()
		secret := assignedSecret
		mu.Unlock()
		assert.True(t, events.VerifyMCPSignature(body, secret, ts, sig), "delivery should sign with the derived secret")

		var event events.Event
		json.Unmarshal(body, &event)
		mu.Lock()
		deliveries = append(deliveries, event)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	c, _ := connectClient(t, srv)

	subscribe := func(clientID string) (string, string) {
		t.Helper()
		raw, err := c.Call("events/subscribe", map[string]any{
			"id":   clientID,
			"name": "discord.message",
			"delivery": map[string]any{
				"mode":   "webhook",
				"url":    callbackSrv.URL,
				"params": map[string]string{"region": "us"},
			},
		})
		require.NoError(t, err)
		var resp struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		}
		require.NoError(t, json.Unmarshal(raw.Raw, &resp))
		return resp.ID, resp.Secret
	}

	id1, sec1 := subscribe("client-id-A")
	id2, sec2 := subscribe("client-id-B") // different client id, same tuple
	assert.Equal(t, id1, id2, "identity mode must derive the same id regardless of client-supplied id")
	assert.Equal(t, sec1, sec2, "identity mode must derive the same secret for the same tuple")
	assert.Len(t, webhooks.Targets(), 1, "two subscribes against the same tuple must collapse to one entry")

	mu.Lock()
	assignedSecret = sec1
	mu.Unlock()

	require.NoError(t, yield(newDiscordEvent("g", "c", "alice", "identity-mode test", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1, "delivery must reach the receiver under the derived secret")
}

// TestE2EWebhookDelivery_StandardHeaders verifies that switching the header
// mode to StandardWebhooks emits webhook-id / webhook-timestamp /
// webhook-signature on the wire and that the signature verifies via the
// Standard Webhooks verifier (not the X-MCP-* one).
func TestE2EWebhookDelivery_StandardHeaders(t *testing.T) {
	srv, _, yield, _ := buildTestStack(
		events.WithWebhookHeaderMode(events.StandardWebhooks),
	)

	var mu sync.Mutex
	var deliveries int
	var assignedSecret string

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		msgID := r.Header.Get("webhook-id")
		ts := r.Header.Get("webhook-timestamp")
		sig := r.Header.Get("webhook-signature")
		assert.NotEmpty(t, msgID, "must emit webhook-id")
		assert.NotEmpty(t, ts, "must emit webhook-timestamp")
		assert.True(t, len(sig) > 3 && sig[:3] == "v1,", "signature must be v1,<base64>")
		assert.Empty(t, r.Header.Get("X-MCP-Signature"), "must NOT emit X-MCP-* headers in standard mode")

		mu.Lock()
		secret := assignedSecret
		mu.Unlock()
		assert.True(t, events.VerifyStandardWebhooksSignature(body, secret, msgID, ts, sig))

		mu.Lock()
		deliveries++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	c, _ := connectClient(t, srv)
	raw, err := c.Call("events/subscribe", map[string]any{
		"id":       "wh-std",
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL},
	})
	require.NoError(t, err)
	var resp struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &resp))
	mu.Lock()
	assignedSecret = resp.Secret
	mu.Unlock()

	require.NoError(t, yield(newDiscordEvent("g", "c", "alice", "standard-headers test", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, deliveries)
}

// TestE2EUnsubscribeBySecret verifies the proof-of-possession unsubscribe
// path works through the JSON-RPC handler — clients can unsubscribe by
// presenting the secret without remembering the server-assigned id.
func TestE2EUnsubscribeBySecret(t *testing.T) {
	srv, _, _, webhooks := buildTestStack()
	c, _ := connectClient(t, srv)

	raw, err := c.Call("events/subscribe", map[string]any{
		"id":       "wh-unsub",
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": "http://localhost:1/sink"},
	})
	require.NoError(t, err)
	var resp struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &resp))
	require.Len(t, webhooks.Targets(), 1)

	_, err = c.Call("events/unsubscribe", map[string]any{
		"delivery": map[string]any{
			"url":    "http://localhost:1/sink",
			"secret": resp.Secret,
		},
	})
	require.NoError(t, err)
	assert.Len(t, webhooks.Targets(), 0, "unsubscribe by secret must remove the matching subscription")
}

// TestE2EResourceByCursor verifies the per-message resource template resolves
// {cursor} to the matching event's payload. Cursor is the addressing scheme
// now (replacing the previous opaque message ID) — documented in the URI.
func TestE2EResourceByCursor(t *testing.T) {
	srv, _, yield, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	require.NoError(t, yield(newDiscordEvent("g", "c", "alice", "first", time.Now())))
	require.NoError(t, yield(newDiscordEvent("g", "c", "bob", "second", time.Now())))

	// Cursors are assigned monotonically by NewMemoryStore — first event = "1".
	text, err := c.ReadResource("discord://message/1")
	require.NoError(t, err)
	var payload DiscordEventData
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	assert.Equal(t, "first", payload.Content)
}
