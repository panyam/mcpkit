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

func buildTestStack() (*server.Server, *MessageStore, *events.WebhookRegistry) {
	store := NewMessageStore(1000)
	webhooks := events.NewWebhookRegistry()

	srv := server.NewServer(
		core.ServerInfo{Name: "discord-events-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, store)
	registerTools(srv, nil) // nil session = test mode
	events.Register(events.Config{
		Sources:  []events.EventSource{newDiscordSource(store)},
		Webhooks: webhooks,
		Server:   srv,
	})

	store.OnMessage = func(msg Message) {
		event := messageToEvent(msg)
		events.Emit(srv, event)
		srv.NotifyResourceUpdated("discord://messages/recent")
		events.EmitToWebhooks(webhooks, event)
	}

	return srv, store, webhooks
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

// TestE2EPollDelivery verifies events/poll with Discord messages.
func TestE2EPollDelivery(t *testing.T) {
	srv, store, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	store.Add("guild-1", "channel-1", "alice", "hello", time.Now())
	store.Add("guild-1", "channel-1", "bob", "world", time.Now())

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

	// Verify Discord event shape — nested author, not flat sender
	var data DiscordEventData
	require.NoError(t, json.Unmarshal(resp.Results[0].Events[0].Data, &data))
	assert.Equal(t, "alice", data.Author.Username)
	assert.Equal(t, "hello", data.Content)
	assert.Equal(t, "guild-1", data.GuildID)
	assert.Equal(t, "channel-1", data.ChannelID)
}

// TestE2EPushDelivery verifies push via SSE broadcast.
func TestE2EPushDelivery(t *testing.T) {
	srv, store, _ := buildTestStack()

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
	store.Add("g", "c", "alice", "push test", time.Now())
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, received, "notifications/events/event")
}

// TestE2EWebhookDelivery verifies webhook delivery with HMAC verification.
func TestE2EWebhookDelivery(t *testing.T) {
	srv, store, webhooks := buildTestStack()

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

	store.Add("g", "c", "bob", "webhook test", time.Now())
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1)
	assert.Equal(t, "discord.message", deliveries[0].Name)
}

// TestE2EEventsList verifies events/list returns the discord.message definition
// with a rich payloadSchema (nested objects, optional fields, enums).
func TestE2EEventsList(t *testing.T) {
	srv, _, _ := buildTestStack()
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
	// PayloadSchema should be present (auto-derived from DiscordEventData)
	assert.NotNil(t, resp.Events[0].PayloadSchema, "payloadSchema should be auto-derived")
}

// TestE2EResourceRead verifies the recent messages resource.
func TestE2EResourceRead(t *testing.T) {
	srv, store, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	store.Add("g", "c", "alice", "resource test", time.Now())

	text, err := c.ReadResource("discord://messages/recent")
	require.NoError(t, err)
	assert.Contains(t, text, "resource test")
}
