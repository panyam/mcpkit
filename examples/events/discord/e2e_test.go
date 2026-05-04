package main

import (
	"context"
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
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestStack constructs a server + source + yield closure used by all
// e2e tests. The source and yield are returned so tests can directly publish
// events without spinning up a Discord session. Optional WebhookOptions
// configure the registry — defaults match the production demo (Server +
// MCPHeaders).
//
// The cursorless typing source is registered alongside discord.message so
// cursor-shape tests can exercise both modes against the same server.
func buildTestStack(whOpts ...events.WebhookOption) (*server.Server, *events.YieldingSource[DiscordEventData], func(DiscordEventData) error, *events.WebhookRegistry) {
	webhooks := events.NewWebhookRegistry(whOpts...)
	source, yield := newDiscordSource()
	typingSource, _ := newDiscordTypingSource()

	srv := server.NewServer(
		core.ServerInfo{Name: "discord-events-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, source)
	registerTools(srv, nil) // nil session = test mode
	events.Register(events.Config{
		Sources:                  []events.EventSource{source, typingSource},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal", // tests don't wire auth; γ-2 spec gate would reject otherwise
	})

	return srv, source, yield, webhooks
}

// buildTestStackWithTyping returns the same wired server but exposes the
// cursorless typing yield function too. Used by the cursorless e2e tests.
func buildTestStackWithTyping(whOpts ...events.WebhookOption) (*server.Server, func(DiscordEventData) error, func(DiscordTypingData) error, *events.WebhookRegistry) {
	webhooks := events.NewWebhookRegistry(whOpts...)
	source, yield := newDiscordSource()
	typingSource, yieldTyping := newDiscordTypingSource()

	srv := server.NewServer(
		core.ServerInfo{Name: "discord-events-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, source)
	registerTools(srv, nil)
	events.Register(events.Config{
		Sources:                  []events.EventSource{source, typingSource},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	return srv, yield, yieldTyping, webhooks
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

// pollResult mirrors the events/poll response (flat top-level shape per
// the spec; no results[] wrapper, no per-result id). Cursor is *string so
// it decodes both `"cursor": "..."` for cursored sources and
// `"cursor": null` for cursorless ones.
type pollResult struct {
	Events    []events.Event `json:"events"`
	Cursor    *string        `json:"cursor"`
	HasMore   bool           `json:"hasMore"`
	Truncated bool           `json:"truncated,omitempty"`
}

// TestE2EPollDelivery verifies events/poll returns events that were yielded
// via the YieldingSource path. Confirms the library's internal Poll handler
// reads through the user-provided EventStore correctly.
func TestE2EPollDelivery(t *testing.T) {
	srv, _, yield, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	require.NoError(t, yield(newDiscordEvent("guild-1", "channel-1", "alice", "hello", time.Now())))
	require.NoError(t, yield(newDiscordEvent("guild-1", "channel-1", "bob", "world", time.Now())))

	// δ-1: flat events/poll request shape per spec L139-149.
	result, err := c.Call("events/poll", map[string]any{
		"name":   "discord.message",
		"cursor": "0",
	})
	require.NoError(t, err)

	var resp pollResult
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Len(t, resp.Events, 2)

	var data DiscordEventData
	require.NoError(t, json.Unmarshal(resp.Events[0].Data, &data))
	assert.Equal(t, "alice", data.Author.Username)
	assert.Equal(t, "hello", data.Content)
	assert.Equal(t, "guild-1", data.GuildID)
	assert.Equal(t, "channel-1", data.ChannelID)
}

// TestE2EStreamDelivery exercises the events/stream push path end-to-end
// via the typed Go SDK helper (eventsclient.Stream). Mirrors what
// walkthrough.go Step 3 does at the demo layer:
//
//   - Open events/stream against discord.message
//   - yield() server-side
//   - Verify OnEvent callback fires with the spec EventOccurrence shape
//   - Stop() the stream and confirm clean shutdown via Done()
//
// This is the E2E coverage for the new push delivery model. The legacy
// broadcast path is still exercised by TestE2EPushDelivery below; both
// stay until ε-6's deprecation lands in η.
func TestE2EStreamDelivery(t *testing.T) {
	srv, _, yield, _ := buildTestStack()
	c, _ := connectClient(t, srv)
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan events.Event, 4)
	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "discord.message",
		OnEvent:   func(ev events.Event) { got <- ev },
	})
	require.NoError(t, err, "Stream open should succeed against the discord demo stack")
	defer stream.Stop()

	require.NoError(t, yield(newDiscordEvent("g1", "c1", "alice", "stream-test", time.Now())))

	select {
	case ev := <-got:
		assert.Equal(t, "discord.message", ev.Name)
		assert.NotEmpty(t, ev.EventID)
		assert.NotNil(t, ev.Cursor, "discord.message is cursored — wire MUST carry a non-null cursor")
	case <-time.After(2 * time.Second):
		t.Fatal("OnEvent never fired within 2s of yield")
	}

	// Verify Stop() closes the goroutine cleanly.
	stream.Stop()
	select {
	case <-stream.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Stream goroutine did not exit within 2s of Stop()")
	}
}

// TestE2EStreamCursorless verifies events/stream against a cursorless
// source delivers events with `cursor: null` on the wire. Mirrors
// walkthrough.go Step 5. Catches a regression where the typed Stream
// callback might silently drop the cursor field or coerce nil to "".
func TestE2EStreamCursorless(t *testing.T) {
	srv, _, yieldTyping, _ := buildTestStackWithTyping()
	c, _ := connectClient(t, srv)
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan events.Event, 4)
	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "discord.typing",
		OnEvent:   func(ev events.Event) { got <- ev },
	})
	require.NoError(t, err)
	defer stream.Stop()

	require.NoError(t, yieldTyping(DiscordTypingData{
		GuildID: "g", ChannelID: "c", User: "alice",
		StartedAt: time.Now().Format(time.RFC3339),
	}))

	select {
	case ev := <-got:
		assert.Equal(t, "discord.typing", ev.Name)
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

	c, _ := connectClient(t, srv)

	clientSecret := events.GenerateSecret()
	subResult, err := c.Call("events/subscribe", map[string]any{
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": clientSecret},
	})
	require.NoError(t, err)
	require.Len(t, webhooks.Targets(), 1)

	// Spec: subscribe response does NOT echo the secret. The client
	// already knows the value it supplied; echoing risks leaks via
	// proxies / logs / IDE network panes during development. The
	// receiver verifies signatures using the value the client sent.
	var subResp struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(subResult.Raw, &subResp))
	require.Empty(t, subResp.Secret, "subscribe response must NOT carry a secret field per spec")
	require.NotEmpty(t, subResult.Raw, "response should still have other fields")
	_ = subResult
	mu.Lock()
	assignedSecret = clientSecret
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
			Cursorless    bool     `json:"cursorless"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	byName := map[string]int{}
	for i, e := range resp.Events {
		byName[e.Name] = i
	}
	require.Contains(t, byName, "discord.message")
	require.Contains(t, byName, "discord.typing")

	msg := resp.Events[byName["discord.message"]]
	assert.ElementsMatch(t, []string{"push", "poll", "webhook"}, msg.Delivery)
	assert.NotNil(t, msg.PayloadSchema, "payloadSchema should be auto-derived")
	assert.False(t, msg.Cursorless, "discord.message is cursored")

	typing := resp.Events[byName["discord.typing"]]
	assert.ElementsMatch(t, []string{"push", "webhook"}, typing.Delivery)
	assert.True(t, typing.Cursorless, "discord.typing is cursorless")
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

// TestE2EWebhookDelivery_StandardHeaders pins the StandardWebhooks header
// path explicitly. Today this is also the default (covered by
// TestE2EWebhookDelivery), but the explicit opt-in test stays so the wire
// format remains pinned regardless of any future default flip.
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
	clientSecret := events.GenerateSecret()
	_, err := c.Call("events/subscribe", map[string]any{
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": clientSecret},
	})
	require.NoError(t, err)
	mu.Lock()
	assignedSecret = clientSecret
	mu.Unlock()

	require.NoError(t, yield(newDiscordEvent("g", "c", "alice", "standard-headers test", time.Now())))
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

	c, _ := connectClient(t, srv)
	clientSecret := events.GenerateSecret()
	_, err := c.Call("events/subscribe", map[string]any{
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": clientSecret},
	})
	require.NoError(t, err)
	mu.Lock()
	assignedSecret = clientSecret
	mu.Unlock()

	require.NoError(t, yield(newDiscordEvent("g", "c", "alice", "mcp-headers test", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, deliveries)
}

// TestE2ECursorlessPushDelivery verifies the cursorless typing source: yield
// triggers a push notification whose Event.cursor is JSON null on the wire.
// Confirms the `*string` cursor field round-trips correctly through the
// notification fanout path.
func TestE2ECursorlessPushDelivery(t *testing.T) {
	srv, _, yieldTyping, _ := buildTestStackWithTyping()

	var mu sync.Mutex
	var receivedRaw []map[string]any

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "cursorless-push", Version: "1.0"},
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
	require.NoError(t, yieldTyping(newDiscordTypingEvent("g", "c", "alice", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, receivedRaw, 1, "push must deliver the typing event")
	cursorVal, present := receivedRaw[0]["cursor"]
	require.True(t, present, "cursor field must be present on the wire (not omitted)")
	assert.Nil(t, cursorVal, "cursorless event must wire as cursor:null")
}

// TestE2ECursorlessWebhookDelivery verifies the cursorless typing source's
// webhook delivery: the POSTed Event JSON has cursor:null. Mirrors the push
// test but through the webhook fanout path.
func TestE2ECursorlessWebhookDelivery(t *testing.T) {
	srv, _, yieldTyping, _ := buildTestStackWithTyping()

	var mu sync.Mutex
	var deliveryBody map[string]any
	var assignedSecret string

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

	clientSecret := events.GenerateSecret()
	raw, err := c.Call("events/subscribe", map[string]any{
		"name":     "discord.typing",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL, "secret": clientSecret},
	})
	require.NoError(t, err)
	var resp struct {
		Cursor *string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &resp))
	assert.Nil(t, resp.Cursor, "subscribe response cursor must be null for cursorless source")
	mu.Lock()
	assignedSecret = clientSecret
	mu.Unlock()
	_ = assignedSecret

	require.NoError(t, yieldTyping(newDiscordTypingEvent("g", "c", "alice", time.Now())))
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, deliveryBody, "webhook must deliver the typing event")
	cursorVal, present := deliveryBody["cursor"]
	require.True(t, present, "cursor field must be present on the wire")
	assert.Nil(t, cursorVal, "cursorless event must wire as cursor:null")
}

// TestE2ECursorlessPollAlwaysEmpty verifies events/poll on a cursorless
// source returns no events and a null cursor regardless of how the client
// addressed it. Subscribers can't replay missed indicators by design.
func TestE2ECursorlessPollAlwaysEmpty(t *testing.T) {
	srv, _, yieldTyping, _ := buildTestStackWithTyping()
	c, _ := connectClient(t, srv)

	for i := 0; i < 3; i++ {
		require.NoError(t, yieldTyping(newDiscordTypingEvent("g", "c", "alice", time.Now())))
	}

	result, err := c.Call("events/poll", map[string]any{
		"name":   "discord.typing",
		"cursor": "0",
	})
	require.NoError(t, err)

	var resp pollResult
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Empty(t, resp.Events)
	assert.Nil(t, resp.Cursor, "poll on cursorless source must return cursor:null")
}

// TestE2ESubscribeCursorNullOnCursoredSourceReturnsLatest verifies the
// "from now" subscribe semantic on a cursored source: passing cursor:null
// makes the server stamp the response with the source's current head, so
// the client polls forward without replaying historical events.
func TestE2ESubscribeCursorNullOnCursoredSourceReturnsLatest(t *testing.T) {
	srv, source, yield, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	require.NoError(t, yield(newDiscordEvent("g", "c", "a", "first", time.Now())))
	require.NoError(t, yield(newDiscordEvent("g", "c", "b", "second", time.Now())))
	expected := source.Latest()
	require.NotEmpty(t, expected, "precondition: source has a head cursor")

	raw, err := c.Call("events/subscribe", map[string]any{
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": "http://localhost:1/sink", "secret": events.GenerateSecret()},
		// cursor field intentionally omitted → JSON null on parse
	})
	require.NoError(t, err)
	var resp struct {
		Cursor *string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &resp))
	require.NotNil(t, resp.Cursor, "cursored source must return a real cursor for cursor:null subscribe")
	assert.Equal(t, expected, *resp.Cursor, "subscribe response cursor must equal source.Latest()")
}

// TestE2EPollMultiSubRejected verifies the wire-level guarantee that
// events/poll no longer accepts multiple subscriptions in one request.
// Single-sub callers still work; multi-sub callers get a clear error.
func TestE2EPollMultiSubRejected(t *testing.T) {
	srv, _, _, _ := buildTestStack()
	c, _ := connectClient(t, srv)

	// δ-1: the {subscriptions: [...]} wrapper itself is rejected with a
	// helpful error — multi-sub vs single-sub no longer matters since the
	// spec's flat shape doesn't have an array at all. The
	// TestPoll_RejectsLegacyWrapper test in wire_shape_test.go covers the
	// wrapper-level rejection at the handler level; this demo test now just
	// confirms the user-facing error message points at the spec.
	_, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"name": "discord.message", "cursor": "0"},
		},
	})
	require.Error(t, err, "legacy {subscriptions: [...]} wrapper must be rejected")
	assert.Contains(t, err.Error(), "legacy", "error message must explain the wrapper rejection")
	assert.Contains(t, err.Error(), "L139", "error must cite the spec section")
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
