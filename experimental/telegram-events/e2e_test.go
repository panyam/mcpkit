package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// buildTestStack returns a fully wired test server plus the source/yield
// pair so tests can publish events directly without a Telegram session.
// Optional WebhookOptions configure the registry; defaults match the
// production demo (Server + MCPHeaders).
func buildTestStack(whOpts ...events.WebhookOption) (*server.Server, *events.YieldingSource[TelegramEventData], func(TelegramEventData) error, *events.WebhookRegistry) {
	webhooks := events.NewWebhookRegistry(whOpts...)
	source, yield := newTelegramSource()

	srv := server.NewServer(
		core.ServerInfo{Name: "telegram-events-e2e", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, source)
	(&ToolDelivery{Bot: nil}).Register(srv)
	events.Register(events.Config{
		Sources:  []events.EventSource{source},
		Webhooks: webhooks,
		Server:   srv,
	})

	return srv, source, yield, webhooks
}

// yieldText is a small helper for tests that just need to publish a message.
func yieldText(yield func(TelegramEventData) error, chatID int64, sender, text string) error {
	return yield(TelegramEventData{
		ChatID:    strconv.FormatInt(chatID, 10),
		User:      sender,
		Text:      text,
		Timestamp: time.Now().Format(time.RFC3339),
	})
}

// TestE2EPollDelivery verifies events/poll returns events that were yielded
// via the YieldingSource path. Confirms the library's internal Poll handler
// reads through the user-provided source correctly with cursor pagination.
func TestE2EPollDelivery(t *testing.T) {
	srv, _, yield, _ := buildTestStack()

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "poll-test", Version: "1.0"})
	require.NoError(t, c.Connect())

	require.NoError(t, yieldText(yield, 100, "alice", "hello"))
	require.NoError(t, yieldText(yield, 100, "bob", "world"))
	require.NoError(t, yieldText(yield, 100, "carol", "!"))

	result, err := c.Call("events/poll", map[string]any{
		"maxEvents": 2,
		"subscriptions": []map[string]any{
			{"id": "poll-1", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Results, 1)
	assert.Len(t, resp.Results[0].Events, 2)
	assert.Equal(t, "2", resp.Results[0].Cursor)

	result2, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "poll-2", "name": "telegram.message", "cursor": resp.Results[0].Cursor},
		},
	})
	require.NoError(t, err)

	var resp2 struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result2.Raw, &resp2))
	assert.Len(t, resp2.Results[0].Events, 1)
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
			defer mu.Unlock()
			received = append(received, method)
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	time.Sleep(200 * time.Millisecond)
	require.NoError(t, yieldText(yield, 100, "alice", "push test"))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, received, "notifications/events/event")
}

// TestE2EWebhookDelivery verifies webhook fanout via the default secret
// mode (Server). Per the post-PR-C contract: server generates the secret
// regardless of what the client supplies, returns it on subscribe, and
// signs deliveries with the generated secret.
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

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "webhook-test", Version: "1.0"})
	require.NoError(t, c.Connect())

	subResult, err := c.Call("events/subscribe", map[string]any{
		"id":       "wh-e2e",
		"name":     "telegram.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": "ignored-in-server-mode"},
	})
	require.NoError(t, err)

	var subResp struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(subResult.Raw, &subResp))
	assert.Equal(t, "wh-e2e", subResp.ID)
	require.NotEmpty(t, subResp.Secret)
	require.NotEqual(t, "ignored-in-server-mode", subResp.Secret, "server mode must NOT echo the client-supplied secret")
	mu.Lock()
	assignedSecret = subResp.Secret
	mu.Unlock()
	require.Len(t, webhooks.Targets(), 1)

	require.NoError(t, yieldText(yield, 200, "bob", "webhook test"))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1)
	assert.Equal(t, "telegram.message", deliveries[0].Name)
}

// TestE2EWebhookDelivery_IdentityMode verifies the identity-mode contract
// end-to-end on telegram-events: subscribe is idempotent on the
// (name, url, params) tuple, server returns derived id and secret, and
// webhook delivery signs with the derived secret.
func TestE2EWebhookDelivery_IdentityMode(t *testing.T) {
	srv, _, yield, webhooks := buildTestStack(
		events.WithWebhookSecretMode(events.WebhookSecretIdentity),
		events.WithWebhookRoot([]byte("test-root-master")),
	)

	var mu sync.Mutex
	var deliveries int
	var assignedSecret string

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-MCP-Signature")
		ts := r.Header.Get("X-MCP-Timestamp")
		mu.Lock()
		secret := assignedSecret
		mu.Unlock()
		assert.True(t, events.VerifyMCPSignature(body, secret, ts, sig))
		mu.Lock()
		deliveries++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "identity-test", Version: "1.0"})
	require.NoError(t, c.Connect())

	subscribe := func(clientID string) (string, string) {
		t.Helper()
		raw, err := c.Call("events/subscribe", map[string]any{
			"id":   clientID,
			"name": "telegram.message",
			"delivery": map[string]any{
				"mode":   "webhook",
				"url":    callbackSrv.URL,
				"params": map[string]string{"chat": "100"},
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
	id1, sec1 := subscribe("client-A")
	id2, sec2 := subscribe("client-B")
	assert.Equal(t, id1, id2, "identity mode derives same id regardless of client-supplied id")
	assert.Equal(t, sec1, sec2, "identity mode derives same secret for same tuple")
	assert.Len(t, webhooks.Targets(), 1, "two subscribes against same tuple collapse to one entry")

	mu.Lock()
	assignedSecret = sec1
	mu.Unlock()
	require.NoError(t, yieldText(yield, 100, "alice", "identity-mode test"))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, deliveries)
}

// TestE2EWebhookDelivery_StandardHeaders verifies StandardWebhooks header
// emission on telegram-events.
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
		assert.NotEmpty(t, msgID)
		assert.NotEmpty(t, ts)
		assert.True(t, len(sig) > 3 && sig[:3] == "v1,")
		assert.Empty(t, r.Header.Get("X-MCP-Signature"))

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

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "stdhdr-test", Version: "1.0"})
	require.NoError(t, c.Connect())

	raw, err := c.Call("events/subscribe", map[string]any{
		"id":       "wh-std",
		"name":     "telegram.message",
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

	require.NoError(t, yieldText(yield, 100, "alice", "standard-headers test"))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, deliveries)
}

// TestE2EEventsList verifies events/list returns the telegram.message
// definition with payloadSchema auto-derived from TelegramEventData.
func TestE2EEventsList(t *testing.T) {
	srv, _, _, _ := buildTestStack()

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "list-test", Version: "1.0"})
	require.NoError(t, c.Connect())

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
	assert.Equal(t, "telegram.message", resp.Events[0].Name)
	assert.ElementsMatch(t, []string{"push", "poll", "webhook"}, resp.Events[0].Delivery)
	assert.NotNil(t, resp.Events[0].PayloadSchema, "payloadSchema should be auto-derived")
}

// TestE2EResourceReadViaClient verifies the recent-messages resource reads
// from the same source that events/poll uses — single source of truth.
func TestE2EResourceReadViaClient(t *testing.T) {
	srv, _, yield, _ := buildTestStack()

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "resource-test", Version: "1.0"})
	require.NoError(t, c.Connect())

	require.NoError(t, yieldText(yield, 100, "alice", "resource test"))

	text, err := c.ReadResource("telegram://messages/recent")
	require.NoError(t, err)
	assert.Contains(t, text, "resource test")

	var payloads []TelegramEventData
	require.NoError(t, json.Unmarshal([]byte(text), &payloads))
	require.Len(t, payloads, 1)
	assert.Equal(t, "alice", payloads[0].User)
}
