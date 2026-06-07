package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestStack wires the same shape as main.go but inside httptest —
// HTTPSource inject endpoints alongside the MCP transport, in-memory
// stores, anonymous principal. Used by every e2e test.
func buildTestStack(t *testing.T) (*httptest.Server, *events.HTTPSource[ChatMessageData], *events.HTTPSource[PresenceChangedData], *events.WebhookRegistry) {
	t.Helper()

	chatSrc := events.NewHTTPSource[ChatMessageData](events.EventDef{
		Name:     "chat.message",
		Delivery: []string{"push", "poll", "webhook"},
	}, events.HTTPSourceConfig{
		YieldingOpts: []events.YieldingOption{events.WithMaxSize(100)},
	})
	presenceSrc := events.NewHTTPSource[PresenceChangedData](events.EventDef{
		Name:     "presence.changed",
		Delivery: []string{"push", "webhook"},
	}, events.HTTPSourceConfig{
		YieldingOpts: []events.YieldingOption{events.WithoutCursors()},
	})

	webhooks := events.NewWebhookRegistry(events.WithWebhookAllowPrivateNetworks(true))

	srv := server.NewServer(
		core.ServerInfo{Name: "whole-enchilada-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, chatSrc)
	events.Register(events.Config{
		Sources:                  []events.EventSource{chatSrc, presenceSrc},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})

	mcpHandler := srv.Handler(server.WithStreamableHTTP(true))
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.Handle(chatSrc.InjectPath(), chatSrc.Handler())
	mux.Handle(presenceSrc.InjectPath(), presenceSrc.Handler())

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, chatSrc, presenceSrc, webhooks
}

func connectClient(t *testing.T, ts *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// pollResultWire mirrors the events/poll response (flat top-level shape).
type pollResultWire struct {
	Events    []events.Event `json:"events"`
	Cursor    *string        `json:"cursor"`
	HasMore   bool           `json:"hasMore"`
	Truncated bool           `json:"truncated,omitempty"`
}

func TestE2E_InitializeSmoke(t *testing.T) {
	ts, _, _, _ := buildTestStack(t)
	c := connectClient(t, ts)
	assert.Equal(t, "whole-enchilada-test", c.ServerInfo.Name)
}

func TestE2E_HTTPInjectAndPoll(t *testing.T) {
	ts, chatSrc, _, _ := buildTestStack(t)
	c := connectClient(t, ts)

	// Push two events via the same HTTP wire the push-server uses.
	pusher := eventsclient.NewPusher(ts.URL, "")
	require.NoError(t, pusher.PushNamed(context.Background(), "chat.message", ChatMessageData{
		Channel: "general", Sender: "alice", Text: "hello", Timestamp: "2026-01-01T00:00:00Z",
	}))
	require.NoError(t, pusher.PushNamed(context.Background(), "chat.message", ChatMessageData{
		Channel: "general", Sender: "bob", Text: "world", Timestamp: "2026-01-01T00:00:01Z",
	}))

	// Confirm the typed buffer captured both (proves end-to-end into
	// the underlying YieldingSource).
	recent := chatSrc.Recent(10)
	require.Len(t, recent, 2)
	assert.Equal(t, "alice", recent[0].Sender)

	// Poll via MCP to confirm the wire surfaces them.
	raw, err := c.Call("events/poll", map[string]any{
		"name":   "chat.message",
		"cursor": "0",
	})
	require.NoError(t, err)
	var pr pollResultWire
	require.NoError(t, json.Unmarshal(raw.Raw, &pr))
	assert.Len(t, pr.Events, 2, "events/poll must surface the two HTTP-injected events")
	assert.False(t, pr.HasMore)
}

func TestE2E_CursorlessPresenceShape(t *testing.T) {
	ts, _, presenceSrc, _ := buildTestStack(t)
	c := connectClient(t, ts)

	pusher := eventsclient.NewPusher(ts.URL, "")
	require.NoError(t, pusher.PushNamed(context.Background(), "presence.changed", PresenceChangedData{
		User: "alice", State: "online", Timestamp: "2026-01-01T00:00:00Z",
	}))

	// The underlying source is cursorless — Def reflects it.
	assert.True(t, presenceSrc.Def().Cursorless)

	// Poll a cursorless source always returns empty + cursor:null.
	raw, err := c.Call("events/poll", map[string]any{
		"name":   "presence.changed",
		"cursor": "0",
	})
	require.NoError(t, err)
	var pr pollResultWire
	require.NoError(t, json.Unmarshal(raw.Raw, &pr))
	assert.Empty(t, pr.Events, "cursorless source must always return empty poll")
	assert.Nil(t, pr.Cursor, "cursorless source must return cursor:null")
}

func TestE2E_WebhookDelivery(t *testing.T) {
	ts, _, _, _ := buildTestStack(t)
	c := connectClient(t, ts)

	// Stand up a tiny webhook receiver: capture deliveries + verify
	// the Standard Webhooks headers are present.
	type captured struct {
		ID   string
		TS   string
		Sig  string
		Body []byte
	}
	ch := make(chan captured, 8)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		ch <- captured{
			ID:   r.Header.Get("webhook-id"),
			TS:   r.Header.Get("webhook-timestamp"),
			Sig:  r.Header.Get("webhook-signature"),
			Body: body.Bytes(),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	// Subscribe to chat.message via webhook.
	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   "chat.message",
		CallbackURL: recv.URL,
	})
	require.NoError(t, err)
	defer sub.Stop()

	// Push an event; receiver captures the delivery.
	pusher := eventsclient.NewPusher(ts.URL, "")
	require.NoError(t, pusher.PushNamed(context.Background(), "chat.message", ChatMessageData{
		Channel: "general", Sender: "alice", Text: "via-webhook", Timestamp: "2026-01-01T00:00:00Z",
	}))

	select {
	case got := <-ch:
		assert.NotEmpty(t, got.ID, "webhook-id header must be present")
		assert.NotEmpty(t, got.TS, "webhook-timestamp header must be present")
		assert.True(t, strings.HasPrefix(got.Sig, "v1,"), "webhook-signature must use Standard Webhooks v1 format, got %q", got.Sig)
		assert.Contains(t, string(got.Body), "via-webhook")
	case <-time.After(3 * time.Second):
		t.Fatal("webhook delivery timed out")
	}
}

func TestE2E_HTTPInjectAndPushStream(t *testing.T) {
	ts, _, _, _ := buildTestStack(t)
	c := connectClient(t, ts)

	// Open a push stream; callback receives the spec EventOccurrence frame.
	gotEvent := make(chan events.Event, 4)
	stream, err := eventsclient.Stream(context.Background(), c, eventsclient.StreamOptions{
		EventName: "chat.message",
		OnEvent:   func(ev events.Event) { gotEvent <- ev },
	})
	require.NoError(t, err)
	defer stream.Stop()

	pusher := eventsclient.NewPusher(ts.URL, "")
	require.NoError(t, pusher.PushNamed(context.Background(), "chat.message", ChatMessageData{
		Channel: "general", Sender: "alice", Text: "via-push", Timestamp: "2026-01-01T00:00:00Z",
	}))

	select {
	case ev := <-gotEvent:
		var payload ChatMessageData
		require.NoError(t, json.Unmarshal(ev.Data, &payload))
		assert.Equal(t, "alice", payload.Sender)
		assert.Equal(t, "via-push", payload.Text)
	case <-time.After(3 * time.Second):
		t.Fatal("push delivery timed out")
	}
}
