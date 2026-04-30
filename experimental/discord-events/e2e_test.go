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
		Sources:  []events.EventSource{source, typingSource},
		Webhooks: webhooks,
		Server:   srv,
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
		Sources:  []events.EventSource{source, typingSource},
		Webhooks: webhooks,
		Server:   srv,
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

	raw, err := c.Call("events/subscribe", map[string]any{
		"id":       "wh-typing",
		"name":     "discord.typing",
		"delivery": map[string]any{"mode": "webhook", "url": callbackSrv.URL},
	})
	require.NoError(t, err)
	var resp struct {
		Cursor *string `json:"cursor"`
		Secret string  `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &resp))
	assert.Nil(t, resp.Cursor, "subscribe response cursor must be null for cursorless source")
	mu.Lock()
	assignedSecret = resp.Secret
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
		"subscriptions": []map[string]any{
			{"id": "p", "name": "discord.typing", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct {
		Results []struct {
			Events []events.Event `json:"events"`
			Cursor *string        `json:"cursor"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Results, 1)
	assert.Empty(t, resp.Results[0].Events)
	assert.Nil(t, resp.Results[0].Cursor, "poll on cursorless source must return cursor:null")
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
		"id":       "wh-fromnow",
		"name":     "discord.message",
		"delivery": map[string]any{"mode": "webhook", "url": "http://localhost:1/sink"},
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

	_, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "a", "name": "discord.message", "cursor": "0"},
			{"id": "b", "name": "discord.typing", "cursor": "0"},
		},
	})
	require.Error(t, err, "multi-subscription events/poll must be rejected")
	assert.Contains(t, err.Error(), "exactly one subscription", "error message must point at the spec change")
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
