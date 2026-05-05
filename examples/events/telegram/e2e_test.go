package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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

// buildTestStack returns a fully wired test server plus the source/yield
// pair so tests can publish events directly without a Telegram session.
// Optional WebhookOptions configure the registry; defaults match the
// production demo (Server + MCPHeaders).
//
// The cursorless typing source is registered alongside telegram.message so
// cursor-shape tests can exercise both modes against the same server.
func buildTestStack(whOpts ...events.WebhookOption) (*server.Server, *events.YieldingSource[TelegramEventData], func(TelegramEventData) error, *events.WebhookRegistry) {
	// ζ-1: tests subscribe to httptest URLs (127.0.0.1:N); bypass the
	// production-default SSRF dial guard.
	whOpts = append([]events.WebhookOption{events.WithWebhookAllowPrivateNetworks(true)}, whOpts...)
	webhooks := events.NewWebhookRegistry(whOpts...)
	source, yield := newTelegramSource()
	typingSource, _ := newTelegramTypingSource()

	srv := server.NewServer(
		core.ServerInfo{Name: "telegram-events-e2e", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, source)
	(&ToolDelivery{Bot: nil}).Register(srv)
	events.Register(events.Config{
		Sources:                  []events.EventSource{source, typingSource},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})

	return srv, source, yield, webhooks
}

// buildTestStackWithTyping is a parallel constructor that returns the typing
// yield closure alongside the message yield. Used by the cursorless e2e
// tests so they can publish typing events without spinning up a Telegram
// session.
func buildTestStackWithTyping() (*server.Server, func(TelegramEventData) error, func(TelegramTypingData) error) {
	webhooks := events.NewWebhookRegistry(events.WithWebhookAllowPrivateNetworks(true))
	source, yield := newTelegramSource()
	typingSource, yieldTyping := newTelegramTypingSource()

	srv := server.NewServer(
		core.ServerInfo{Name: "telegram-events-e2e", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, source)
	(&ToolDelivery{Bot: nil}).Register(srv)
	events.Register(events.Config{
		Sources:                  []events.EventSource{source, typingSource},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	return srv, yield, yieldTyping
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

	// δ-1: flat events/poll request shape per spec L139-149.
	result, err := c.Call("events/poll", map[string]any{
		"name":      "telegram.message",
		"cursor":    "0",
		"maxEvents": 2,
	})
	require.NoError(t, err)

	var resp pollResult
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Len(t, resp.Events, 2)
	require.NotNil(t, resp.Cursor)
	assert.Equal(t, "2", *resp.Cursor)

	result2, err := c.Call("events/poll", map[string]any{
		"name":   "telegram.message",
		"cursor": *resp.Cursor,
	})
	require.NoError(t, err)

	var resp2 pollResult
	require.NoError(t, json.Unmarshal(result2.Raw, &resp2))
	assert.Len(t, resp2.Events, 1)
}

// TestE2EStreamDelivery exercises events/stream end-to-end via the typed
// Go SDK helper (eventsclient.Stream), mirroring walkthrough Step 2 — open
// a stream, yield, observe OnEvent, Stop cleanly.
//
// Verifies the new push delivery model works against the telegram demo
// stack — same wire shape as discord (which has its own e2e test) but
// against the telegram payload.
func TestE2EStreamDelivery(t *testing.T) {
	srv, _, yield, _ := buildTestStack()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "stream-test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan events.Event, 4)
	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "telegram.message",
		OnEvent:   func(ev events.Event) { got <- ev },
	})
	require.NoError(t, err)
	defer stream.Stop()

	require.NoError(t, yieldText(yield, 100, "alice", "stream-test"))

	select {
	case ev := <-got:
		assert.Equal(t, "telegram.message", ev.Name)
		assert.NotEmpty(t, ev.EventID)
		assert.NotNil(t, ev.Cursor, "telegram.message is cursored — wire MUST carry a non-null cursor")
	case <-time.After(2 * time.Second):
		t.Fatal("OnEvent never fired within 2s of yield")
	}

	stream.Stop()
	select {
	case <-stream.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Stream goroutine did not exit within 2s of Stop()")
	}
}

// TestE2EStreamCursorless verifies events/stream against the cursorless
// telegram.typing source delivers events with cursor:null on the wire.
// Mirrors walkthrough Step 3.
func TestE2EStreamCursorless(t *testing.T) {
	srv, _, yieldTyping := buildTestStackWithTyping()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "cursorless-test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan events.Event, 4)
	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "telegram.typing",
		OnEvent:   func(ev events.Event) { got <- ev },
	})
	require.NoError(t, err)
	defer stream.Stop()

	require.NoError(t, yieldTyping(TelegramTypingData{
		ChatID: "100", User: "alice",
		StartedAt: time.Now().Format(time.RFC3339),
	}))

	select {
	case ev := <-got:
		assert.Equal(t, "telegram.typing", ev.Name)
		assert.Nil(t, ev.Cursor, "cursorless source MUST emit cursor:null on the wire (spec L294)")
	case <-time.After(2 * time.Second):
		t.Fatal("OnEvent never fired within 2s of yieldTyping")
	}
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

// TestE2EWebhookDelivery verifies webhook fanout via the default modes:
// Server-generated secret + StandardWebhooks header naming. The latter is
// the post-r3167245184 default (upstream WG PR#1 line 434, author aligned
// on Standard Webhooks).
func TestE2EWebhookDelivery(t *testing.T) {
	srv, _, yield, webhooks := buildTestStack()

	var mu sync.Mutex
	var deliveries []events.Event
	var assignedSecret string

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		msgID := r.Header.Get("webhook-id")
		ts := r.Header.Get("webhook-timestamp")
		sig := r.Header.Get("webhook-signature")
		mu.Lock()
		secret := assignedSecret
		mu.Unlock()
		assert.True(t, events.VerifyStandardWebhooksSignature(body, secret, msgID, ts, sig), "signature should verify against the server-assigned secret")
		assert.Empty(t, r.Header.Get("X-MCP-Signature"), "default header mode must NOT emit X-MCP-* headers")

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

	clientSecret := events.GenerateSecret()
	subResult, err := c.Call("events/subscribe", map[string]any{
		"name":     "telegram.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": clientSecret},
	})
	require.NoError(t, err)

	// Spec: subscribe response carries id (server-derived per
	// §"Subscription Identity" → "Derived id" L367) but does NOT echo
	// the secret. The client already supplied the secret; receiver
	// verifies with that value. The id is the X-MCP-Subscription-Id
	// routing handle (γ-4 wires the header).
	var subResp struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(subResult.Raw, &subResp))
	assert.True(t, strings.HasPrefix(subResp.ID, "sub_"), "id must be the server-derived sub_<base64> per spec; got %q", subResp.ID)
	require.Empty(t, subResp.Secret, "subscribe response must NOT carry a secret field per spec")
	mu.Lock()
	assignedSecret = clientSecret
	mu.Unlock()
	require.Len(t, webhooks.Targets(), 1)

	require.NoError(t, yieldText(yield, 200, "bob", "webhook test"))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 1)
	assert.Equal(t, "telegram.message", deliveries[0].Name)
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

	clientSecret := events.GenerateSecret()
	_, err := c.Call("events/subscribe", map[string]any{
		"name":     "telegram.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": clientSecret},
	})
	require.NoError(t, err)
	mu.Lock()
	assignedSecret = clientSecret
	mu.Unlock()

	require.NoError(t, yieldText(yield, 100, "alice", "standard-headers test"))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, deliveries)
}

// TestE2EWebhookDelivery_MCPHeadersOptIn pins the MCPHeaders header path
// explicitly. Symmetric to TestE2EWebhookDelivery_StandardHeaders — the
// non-default mode is opted into via WithWebhookHeaderMode and verified
// for byte-format on the wire.
func TestE2EWebhookDelivery_MCPHeadersOptIn(t *testing.T) {
	srv, _, yield, _ := buildTestStack(
		events.WithWebhookHeaderMode(events.MCPHeaders),
	)

	var mu sync.Mutex
	var deliveries int
	var assignedSecret string

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-MCP-Signature")
		ts := r.Header.Get("X-MCP-Timestamp")
		assert.NotEmpty(t, sig, "must emit X-MCP-Signature")
		assert.NotEmpty(t, ts, "must emit X-MCP-Timestamp")
		assert.True(t, len(sig) > 7 && sig[:7] == "sha256=", "signature must be sha256=<hex>")
		assert.Empty(t, r.Header.Get("webhook-signature"), "must NOT emit webhook-* headers in MCP mode")

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
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "mcphdr-test", Version: "1.0"})
	require.NoError(t, c.Connect())

	clientSecret := events.GenerateSecret()
	_, err := c.Call("events/subscribe", map[string]any{
		"name":     "telegram.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": clientSecret},
	})
	require.NoError(t, err)
	mu.Lock()
	assignedSecret = clientSecret
	mu.Unlock()

	require.NoError(t, yieldText(yield, 100, "alice", "mcp-headers test"))
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
			Cursorless    bool     `json:"cursorless"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	byName := map[string]int{}
	for i, e := range resp.Events {
		byName[e.Name] = i
	}
	require.Contains(t, byName, "telegram.message")
	require.Contains(t, byName, "telegram.typing")

	msg := resp.Events[byName["telegram.message"]]
	assert.ElementsMatch(t, []string{"push", "poll", "webhook"}, msg.Delivery)
	assert.NotNil(t, msg.PayloadSchema, "payloadSchema should be auto-derived")
	assert.False(t, msg.Cursorless, "telegram.message is cursored")

	typing := resp.Events[byName["telegram.typing"]]
	assert.ElementsMatch(t, []string{"push", "webhook"}, typing.Delivery)
	assert.True(t, typing.Cursorless, "telegram.typing is cursorless")
}

// TestE2ECursorlessPushDelivery verifies the cursorless typing source on
// telegram-events: yield triggers a push notification whose Event.cursor
// is JSON null on the wire.
func TestE2ECursorlessPushDelivery(t *testing.T) {
	srv, _, yieldTyping := buildTestStackWithTyping()

	var mu sync.Mutex
	var receivedRaw []map[string]any

	handler := srv.Handler(server.WithStreamableHTTP(true))
	tsrv := httptest.NewServer(handler)
	defer tsrv.Close()

	c := client.NewClient(tsrv.URL+"/mcp", core.ClientInfo{Name: "cursorless-push", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithNotificationCallback(func(method string, params any) {
			if method != "notifications/events/event" {
				return
			}
			b, _ := json.Marshal(params)
			var p map[string]any
			_ = json.Unmarshal(b, &p)
			mu.Lock()
			receivedRaw = append(receivedRaw, p)
			mu.Unlock()
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	time.Sleep(200 * time.Millisecond)
	require.NoError(t, yieldTyping(newTelegramTypingEvent(100, "alice", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, receivedRaw, 1, "push must deliver the typing event")
	cursorVal, present := receivedRaw[0]["cursor"]
	require.True(t, present, "cursor field must be present on the wire")
	assert.Nil(t, cursorVal, "cursorless event must wire as cursor:null")
}

// TestE2ECursorlessWebhookDelivery verifies the cursorless typing source's
// webhook delivery on telegram-events: the POSTed Event JSON has cursor:null.
func TestE2ECursorlessWebhookDelivery(t *testing.T) {
	srv, _, yieldTyping := buildTestStackWithTyping()

	var mu sync.Mutex
	var deliveryBody map[string]any

	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &deliveryBody)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	handler := srv.Handler(server.WithStreamableHTTP(true))
	tsrv := httptest.NewServer(handler)
	defer tsrv.Close()
	c := client.NewClient(tsrv.URL+"/mcp", core.ClientInfo{Name: "cursorless-wh", Version: "1.0"})
	require.NoError(t, c.Connect())

	raw, err := c.Call("events/subscribe", map[string]any{
		"name":     "telegram.typing",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": events.GenerateSecret()},
	})
	require.NoError(t, err)
	var resp struct {
		Cursor *string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &resp))
	assert.Nil(t, resp.Cursor, "subscribe response cursor must be null for cursorless source")

	require.NoError(t, yieldTyping(newTelegramTypingEvent(100, "alice", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, deliveryBody)
	cursorVal, present := deliveryBody["cursor"]
	require.True(t, present)
	assert.Nil(t, cursorVal, "cursorless event must wire as cursor:null")
}

// TestE2EPollMultiSubRejected verifies the wire-level guarantee that
// events/poll no longer accepts multiple subscriptions in one request.
// Mirrors the discord-events test for telegram parity.
func TestE2EPollMultiSubRejected(t *testing.T) {
	srv, _, _, _ := buildTestStack()

	handler := srv.Handler(server.WithStreamableHTTP(true))
	tsrv := httptest.NewServer(handler)
	defer tsrv.Close()
	c := client.NewClient(tsrv.URL+"/mcp", core.ClientInfo{Name: "multi-sub", Version: "1.0"})
	require.NoError(t, c.Connect())

	// δ-1: the {subscriptions: [...]} wrapper is rejected with a helpful
	// error pointing at the spec change (L139-149).
	_, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"name": "telegram.message", "cursor": "0"},
		},
	})
	require.Error(t, err, "legacy {subscriptions: [...]} wrapper must be rejected")
	assert.Contains(t, err.Error(), "legacy")
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
