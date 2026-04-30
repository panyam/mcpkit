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
func buildTestStack() (*server.Server, *events.YieldingSource[TelegramEventData], func(TelegramEventData) error, *events.WebhookRegistry) {
	webhooks := events.NewWebhookRegistry()
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

// TestE2EWebhookDelivery verifies webhook fanout — same library hook also
// routes events to registered webhook subscribers with HMAC signing.
func TestE2EWebhookDelivery(t *testing.T) {
	srv, _, yield, webhooks := buildTestStack()

	var mu sync.Mutex
	var deliveries []events.Event

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-MCP-Signature")
		ts := r.Header.Get("X-MCP-Timestamp")
		assert.True(t, events.VerifySignature(body, "wh-secret", ts, sig))

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
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": "wh-secret"},
	})
	require.NoError(t, err)

	var subResp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(subResult.Raw, &subResp))
	assert.Equal(t, "wh-e2e", subResp.ID)
	require.Len(t, webhooks.Targets(), 1)

	require.NoError(t, yieldText(yield, 200, "bob", "webhook test"))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1)
	assert.Equal(t, "telegram.message", deliveries[0].Name)
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
