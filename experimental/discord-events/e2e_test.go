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
// events without spinning up a Discord session.
func buildTestStack() (*server.Server, *events.YieldingSource[DiscordEventData], func(DiscordEventData) error, *events.WebhookRegistry) {
	webhooks := events.NewWebhookRegistry()
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

// TestE2EWebhookDelivery verifies webhook fanout — same library hook also
// routes events to registered webhook subscribers with HMAC signing. Tests
// the full subscribe → yield → POST → verify-signature path.
func TestE2EWebhookDelivery(t *testing.T) {
	srv, _, yield, webhooks := buildTestStack()

	var mu sync.Mutex
	var deliveries []events.Event

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-MCP-Signature")
		ts := r.Header.Get("X-MCP-Timestamp")
		assert.True(t, events.VerifySignature(body, "secret", ts, sig))

		var event events.Event
		json.Unmarshal(body, &event)
		mu.Lock()
		deliveries = append(deliveries, event)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	c, _ := connectClient(t, srv)

	_, err := c.Call("events/subscribe", map[string]any{
		"id":       "wh",
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": "secret"},
	})
	require.NoError(t, err)
	require.Len(t, webhooks.Targets(), 1)

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
