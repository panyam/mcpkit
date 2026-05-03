package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preloadedSource constructs a YieldingSource pre-populated with n test
// messages — the test fixture replacement for the old newTestStore helper.
func preloadedSource(n int) (*events.YieldingSource[TelegramEventData], func(TelegramEventData) error) {
	source, yield := newTelegramSource()
	for i := 1; i <= n; i++ {
		_ = yield(TelegramEventData{
			ChatID:    "100",
			MessageID: strconv.Itoa(i),
			User:      "testuser",
			Text:      fmt.Sprintf("message %d", i),
			Timestamp: time.Unix(int64(1000+i), 0).Format(time.RFC3339),
		})
	}
	return source, yield
}

// newConnectedClient creates a fully wired MCP server, starts it on httptest,
// connects a client, and returns both. Used by the handler-level tests below.
func newConnectedClient(t *testing.T, source *events.YieldingSource[TelegramEventData], webhooks *events.WebhookRegistry) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := server.NewServer(
		core.ServerInfo{Name: "telegram-events-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, source)
	(&ToolDelivery{Bot: nil}).Register(srv)
	events.Register(events.Config{
		Sources:                  []events.EventSource{source},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	return c, ts
}

// pollResult mirrors the events/poll response (flat top-level shape per
// the spec; no results[] wrapper, no per-result id). Cursor is *string so
// it decodes both `"cursor": "..."` and `"cursor": null`.
type pollResult struct {
	Events          []events.Event `json:"events"`
	Cursor          *string        `json:"cursor"`
	HasMore         bool           `json:"hasMore"`
	Truncated       bool           `json:"truncated,omitempty"`
	NextPollSeconds int            `json:"nextPollSeconds"`
}

// TestEventsPollCursorPagination verifies events/poll cursor pagination works
// end-to-end against the YieldingSource path. Two pages of 5 cover all 10.
func TestEventsPollCursorPagination(t *testing.T) {
	source, _ := preloadedSource(10)
	c, _ := newConnectedClient(t, source, events.NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"maxEvents": 5,
		"subscriptions": []map[string]any{
			{"id": "page1", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp pollResult
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Len(t, resp.Events, 5)
	require.NotNil(t, resp.Cursor)
	assert.Equal(t, "5", *resp.Cursor)

	result2, err := c.Call("events/poll", map[string]any{
		"maxEvents": 5,
		"subscriptions": []map[string]any{
			{"id": "page2", "name": "telegram.message", "cursor": *resp.Cursor},
		},
	})
	require.NoError(t, err)

	var resp2 pollResult
	require.NoError(t, json.Unmarshal(result2.Raw, &resp2))
	assert.Len(t, resp2.Events, 5)
	require.NotNil(t, resp2.Cursor)
	assert.Equal(t, "10", *resp2.Cursor)
}

// TestEventsPollEmptyStore verifies polling an empty source returns no events
// and a stable cursor — sanity check for the no-data case.
func TestEventsPollEmptyStore(t *testing.T) {
	source, _ := newTelegramSource()
	c, _ := newConnectedClient(t, source, events.NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "empty", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp pollResult
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Empty(t, resp.Events)
	require.NotNil(t, resp.Cursor)
	assert.Equal(t, "0", *resp.Cursor)
}

// TestEventsPollUnknownEvent verifies unknown event names surface as a
// top-level JSON-RPC error (-32011 EventNotFound per spec), not as an
// embedded per-result error from the legacy partial-success model.
// Single-sub call, single-sub response, single-sub error path.
func TestEventsPollUnknownEvent(t *testing.T) {
	source, _ := newTelegramSource()
	c, _ := newConnectedClient(t, source, events.NewWebhookRegistry())

	_, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "bogus", "name": "nonexistent.event", "cursor": "0"},
		},
	})
	require.Error(t, err, "unknown event must return a JSON-RPC error")
	rpcErr, ok := err.(*client.RPCError)
	require.True(t, ok, "error should be RPCError, got %T", err)
	assert.Equal(t, events.ErrCodeEventNotFound, rpcErr.Code, "spec code -32011 EventNotFound")
}

// TestResourceRecentMessages verifies the recent messages resource returns
// typed payloads from the YieldingSource — no separate buffer involved.
func TestResourceRecentMessages(t *testing.T) {
	source, _ := preloadedSource(3)
	c, _ := newConnectedClient(t, source, events.NewWebhookRegistry())

	text, err := c.ReadResource("telegram://messages/recent")
	require.NoError(t, err)

	var payloads []TelegramEventData
	require.NoError(t, json.Unmarshal([]byte(text), &payloads))
	assert.Len(t, payloads, 3)
	assert.Equal(t, "message 1", payloads[0].Text)
}

// TestResourceMessageByCursor verifies the per-message template resolves
// {cursor} to the matching event payload. Cursor is the addressing scheme.
func TestResourceMessageByCursor(t *testing.T) {
	source, _ := preloadedSource(3)
	c, _ := newConnectedClient(t, source, events.NewWebhookRegistry())

	text, err := c.ReadResource("telegram://message/2")
	require.NoError(t, err)

	var payload TelegramEventData
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	assert.Equal(t, "message 2", payload.Text)
}

// TestWebhookHMACSignature_MCPHeaders pins the X-MCP-* wire shape under the
// MCPHeaders opt-in. After the default flip to Standard Webhooks
// (r3167245184), the registry must be explicitly configured with
// WithWebhookHeaderMode(MCPHeaders) for this byte-format check to apply.
func TestWebhookHMACSignature_MCPHeaders(t *testing.T) {
	secret := "test-secret-key"
	var receivedBody []byte
	var receivedSig, receivedTS string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-MCP-Signature")
		receivedTS = r.Header.Get("X-MCP-Timestamp")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	webhooks := events.NewWebhookRegistry(events.WithWebhookHeaderMode(events.MCPHeaders))
	// Direct registry poke (skip the JSON-RPC subscribe handler) — γ-2
	// rekeyed Register on canonical-tuple bytes. Use a stub key for the
	// HMAC delivery test; the canonical-key contents don't matter here.
	webhooks.Register([]byte("hmac-test"), "sub_hmac_test", srv.URL, secret)

	event := events.MakeEvent("telegram.message", "evt_1", "1", time.Now(),
		map[string]string{"text": "hello"})
	webhooks.Deliver(event)

	time.Sleep(100 * time.Millisecond)

	require.NotEmpty(t, receivedBody)
	require.NotEmpty(t, receivedSig)
	require.NotEmpty(t, receivedTS)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(receivedTS))
	mac.Write([]byte("."))
	mac.Write(receivedBody)
	expectedSig := fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
	assert.Equal(t, expectedSig, receivedSig)
}

// TestEventsPollHasMore verifies hasMore is set when maxEvents truncates the
// result. Critical for clients deciding whether to poll again immediately.
func TestEventsPollHasMore(t *testing.T) {
	source, _ := preloadedSource(5)
	c, _ := newConnectedClient(t, source, events.NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"maxEvents": 3,
		"subscriptions": []map[string]any{
			{"id": "hm", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp pollResult
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Len(t, resp.Events, 3)
	assert.True(t, resp.HasMore)

	result2, err := c.Call("events/poll", map[string]any{
		"maxEvents": 3,
		"subscriptions": []map[string]any{
			{"id": "hm2", "name": "telegram.message", "cursor": *resp.Cursor},
		},
	})
	require.NoError(t, err)

	var resp2 pollResult
	require.NoError(t, json.Unmarshal(result2.Raw, &resp2))
	assert.Len(t, resp2.Events, 2)
	assert.False(t, resp2.HasMore)
}

// TestSubscribeReturnsRefreshBefore verifies the mandatory refreshBefore
// field is populated and in the future — clients use this to schedule TTL
// refresh.
func TestSubscribeReturnsRefreshBefore(t *testing.T) {
	source, _ := newTelegramSource()
	webhooks := events.NewWebhookRegistry()
	c, _ := newConnectedClient(t, source, webhooks)

	result, err := c.Call("events/subscribe", map[string]any{
		"id":       "rb-test",
		"name":     "telegram.message",
		"delivery": map[string]any{"mode": "webhook", "url": "http://example.com/hook", "secret": events.GenerateSecret()},
	})
	require.NoError(t, err)

	var resp struct {
		ID            string `json:"id"`
		RefreshBefore string `json:"refreshBefore"`
	}
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	// γ-2: server-derived id replaces client-supplied id per spec
	// §"Subscription Identity" → "Derived id" L367.
	assert.True(t, strings.HasPrefix(resp.ID, "sub_"), "id must be server-derived sub_<base64>; got %q", resp.ID)
	assert.NotEmpty(t, resp.RefreshBefore)

	rb, err := time.Parse(time.RFC3339, resp.RefreshBefore)
	require.NoError(t, err)
	assert.True(t, rb.After(time.Now()))
}

// TestWebhookKeyedByCanonicalTuple verifies γ-2 registry keying: two
// distinct canonical keys (different principals subscribing to the same
// URL) coexist as distinct entries; Unregister by canonical key removes
// only the matching entry. This is the spec's cross-tenant isolation
// property (§"Subscription Identity" → "Cross-tenant isolation" L378)
// at the registry level.
func TestWebhookKeyedByCanonicalTuple(t *testing.T) {
	webhooks := events.NewWebhookRegistry()
	keyA := []byte("alice\x1fhttp://example.com/hook\x1ftelegram.message\x1f{}")
	keyB := []byte("bob\x1fhttp://example.com/hook\x1ftelegram.message\x1f{}")
	webhooks.Register(keyA, "sub_alice", "http://example.com/hook", "whsec_secret-1")
	webhooks.Register(keyB, "sub_bob", "http://example.com/hook", "whsec_secret-2")

	targets := webhooks.Targets()
	assert.Len(t, targets, 2)

	webhooks.Unregister(keyA)
	targets = webhooks.Targets()
	assert.Len(t, targets, 1)
	assert.Equal(t, "sub_bob", targets[0].ID, "unregister(keyA) must leave keyB intact")
}

// TestWebhookTTLExpiry verifies the test-helper ExpireAll path does what
// it claims — used by other tests that exercise post-TTL behavior.
func TestWebhookTTLExpiry(t *testing.T) {
	webhooks := events.NewWebhookRegistry()
	webhooks.Register([]byte("exp-test"), "sub_exp", "http://example.com/hook", "whsec_secret")
	assert.Len(t, webhooks.Targets(), 1)

	webhooks.ExpireAll()
	assert.Empty(t, webhooks.Targets())
}

// TestWebhookRetryOnServerError verifies retry-with-backoff on 5xx responses
// per spec. Three attempts: first two return 500, third returns 200.
func TestWebhookRetryOnServerError(t *testing.T) {
	var mu sync.Mutex
	var attempts int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	webhooks := events.NewWebhookRegistry()
	webhooks.Register([]byte("retry-test"), "sub_retry", srv.URL, "whsec_secret")

	event := events.MakeEvent("telegram.message", "evt_retry", "1", time.Now(),
		map[string]string{"text": "retry me"})
	webhooks.Deliver(event)

	time.Sleep(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, attempts)
}

// TestWebhookNoRetryOn4xx verifies 4xx responses are NOT retried — receivers
// signaling a permanent client error should stop, per spec.
func TestWebhookNoRetryOn4xx(t *testing.T) {
	var mu sync.Mutex
	var attempts int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	webhooks := events.NewWebhookRegistry()
	webhooks.Register([]byte("no-retry"), "sub_no_retry", srv.URL, "whsec_secret")

	event := events.MakeEvent("telegram.message", "evt_4xx", "1", time.Now(),
		map[string]string{"text": "no retry"})
	webhooks.Deliver(event)

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, attempts)
}

// TestVerifySignature verifies the HMAC verification helper used by clients
// to validate incoming webhooks.
func TestVerifySignature(t *testing.T) {
	secret := "my-secret"
	ts := "1700000000"
	body := []byte(`{"test":"data"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))

	assert.True(t, events.VerifySignature(body, secret, ts, sig))
	assert.False(t, events.VerifySignature(body, "wrong-secret", ts, sig))
	assert.False(t, events.VerifySignature([]byte("tampered"), secret, ts, sig))
	assert.False(t, events.VerifySignature(body, secret, "wrong-ts", sig))
}
