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

// newTestStore creates a MessageStore pre-loaded with n messages.
func newTestStore(n int) *MessageStore {
	store := NewMessageStore(1000)
	for i := 1; i <= n; i++ {
		store.Add(100, "testuser", fmt.Sprintf("message %d", i), time.Unix(int64(1000+i), 0))
	}
	return store
}

// newConnectedClient creates a fully wired MCP server, starts it on httptest,
// connects a client, and returns both.
func newConnectedClient(t *testing.T, store *MessageStore, webhooks *events.WebhookRegistry) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := server.NewServer(
		core.ServerInfo{Name: "telegram-events-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, store)
	(&ToolDelivery{Bot: nil}).Register(srv, store)
	events.Register(events.Config{
		Sources:  []events.EventSource{newTelegramSource(store)},
		Webhooks: webhooks,
		Server:   srv,
	})

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	return c, ts
}

// pollResult is the structure returned by events/poll for a single subscription.
type pollResult struct {
	ID              string         `json:"id"`
	Events          []events.Event `json:"events"`
	Cursor          string         `json:"cursor"`
	HasMore         bool           `json:"hasMore"`
	NextPollSeconds int            `json:"nextPollSeconds"`
}

// TestEventsPollCursorPagination verifies events/poll cursor pagination.
func TestEventsPollCursorPagination(t *testing.T) {
	store := newTestStore(10)
	c, _ := newConnectedClient(t, store, events.NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"maxEvents": 5,
		"subscriptions": []map[string]any{
			{"id": "page1", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Results, 1)
	assert.Len(t, resp.Results[0].Events, 5)
	assert.Equal(t, "5", resp.Results[0].Cursor)

	result2, err := c.Call("events/poll", map[string]any{
		"maxEvents": 5,
		"subscriptions": []map[string]any{
			{"id": "page2", "name": "telegram.message", "cursor": resp.Results[0].Cursor},
		},
	})
	require.NoError(t, err)

	var resp2 struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result2.Raw, &resp2))
	assert.Len(t, resp2.Results[0].Events, 5)
	assert.Equal(t, "10", resp2.Results[0].Cursor)
}

// TestEventsPollEmptyStore verifies polling an empty store.
func TestEventsPollEmptyStore(t *testing.T) {
	store := NewMessageStore(1000)
	c, _ := newConnectedClient(t, store, events.NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "empty", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Results, 1)
	assert.Empty(t, resp.Results[0].Events)
	assert.Equal(t, "0", resp.Results[0].Cursor)
}

// TestEventsPollUnknownEvent verifies unknown event name returns per-sub error.
func TestEventsPollUnknownEvent(t *testing.T) {
	store := NewMessageStore(1000)
	c, _ := newConnectedClient(t, store, events.NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "bogus", "name": "nonexistent.event", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct {
		Results []struct {
			ID    string `json:"id"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Results, 1)
	require.NotNil(t, resp.Results[0].Error)
	assert.Equal(t, -32001, resp.Results[0].Error.Code)
}

// TestResourceRecentMessages verifies the recent messages resource.
func TestResourceRecentMessages(t *testing.T) {
	store := newTestStore(3)
	c, _ := newConnectedClient(t, store, events.NewWebhookRegistry())

	text, err := c.ReadResource("telegram://messages/recent")
	require.NoError(t, err)

	var msgs []Message
	require.NoError(t, json.Unmarshal([]byte(text), &msgs))
	assert.Len(t, msgs, 3)
	assert.Equal(t, "message 1", msgs[0].Text)
}

// TestResourceMessageByID verifies the message-by-ID template resource.
func TestResourceMessageByID(t *testing.T) {
	store := newTestStore(3)
	c, _ := newConnectedClient(t, store, events.NewWebhookRegistry())

	text, err := c.ReadResource("telegram://message/2")
	require.NoError(t, err)

	var msg Message
	require.NoError(t, json.Unmarshal([]byte(text), &msg))
	assert.Equal(t, int64(2), msg.ID)
	assert.Equal(t, "message 2", msg.Text)
}

// TestWebhookHMACSignature verifies HMAC-SHA256(secret, ts + "." + body) signing.
func TestWebhookHMACSignature(t *testing.T) {
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

	webhooks := events.NewWebhookRegistry()
	webhooks.Register("hmac-test", srv.URL, secret)

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

// TestMessageStoreCursorSemantics verifies cursor-based retrieval.
func TestMessageStoreCursorSemantics(t *testing.T) {
	store := NewMessageStore(100)
	store.Add(1, "alice", "first", time.Now())
	store.Add(1, "bob", "second", time.Now())
	store.Add(1, "carol", "third", time.Now())

	pr := store.GetSince(0, 100)
	assert.Len(t, pr.Messages, 3)
	assert.Equal(t, int64(3), pr.NextCursor)
	assert.False(t, pr.CursorGap)

	pr = store.GetSince(1, 100)
	assert.Len(t, pr.Messages, 2)
	assert.Equal(t, "second", pr.Messages[0].Text)

	pr = store.GetSince(3, 100)
	assert.Empty(t, pr.Messages)
	assert.Equal(t, int64(3), pr.NextCursor)
}

// TestMessageStoreCursorGap verifies stale cursor detection.
func TestMessageStoreCursorGap(t *testing.T) {
	store := NewMessageStore(3)
	for i := 1; i <= 3; i++ {
		store.Add(1, "user", fmt.Sprintf("msg %d", i), time.Now())
	}
	pr := store.GetSince(2, 100)
	assert.False(t, pr.CursorGap)

	for i := 4; i <= 10; i++ {
		store.Add(1, "user", fmt.Sprintf("msg %d", i), time.Now())
	}
	pr = store.GetSince(2, 100)
	assert.True(t, pr.CursorGap)
	assert.Len(t, pr.Messages, 3)
	assert.Equal(t, "msg 8", pr.Messages[0].Text)
}

// TestMessageStoreRingBuffer verifies ring buffer eviction.
func TestMessageStoreRingBuffer(t *testing.T) {
	store := NewMessageStore(3)
	for i := 1; i <= 5; i++ {
		store.Add(1, "user", fmt.Sprintf("msg %d", i), time.Now())
	}
	assert.Equal(t, 3, store.Len())
	recent := store.Recent(10)
	assert.Len(t, recent, 3)
	assert.Equal(t, "msg 3", recent[0].Text)
	assert.Equal(t, "msg 5", recent[2].Text)
}

// TestEventsPollHasMore verifies hasMore when maxEvents truncates results.
func TestEventsPollHasMore(t *testing.T) {
	store := newTestStore(5)
	c, _ := newConnectedClient(t, store, events.NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"maxEvents": 3,
		"subscriptions": []map[string]any{
			{"id": "hm", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Len(t, resp.Results[0].Events, 3)
	assert.True(t, resp.Results[0].HasMore)

	result2, err := c.Call("events/poll", map[string]any{
		"maxEvents": 3,
		"subscriptions": []map[string]any{
			{"id": "hm2", "name": "telegram.message", "cursor": resp.Results[0].Cursor},
		},
	})
	require.NoError(t, err)

	var resp2 struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result2.Raw, &resp2))
	assert.Len(t, resp2.Results[0].Events, 2)
	assert.False(t, resp2.Results[0].HasMore)
}

// TestSubscribeReturnsRefreshBefore verifies mandatory refreshBefore field.
func TestSubscribeReturnsRefreshBefore(t *testing.T) {
	store := NewMessageStore(1000)
	webhooks := events.NewWebhookRegistry()
	c, _ := newConnectedClient(t, store, webhooks)

	result, err := c.Call("events/subscribe", map[string]any{
		"id":       "rb-test",
		"name":     "telegram.message",
		"delivery": map[string]any{"mode": "webhook", "url": "http://example.com/hook", "secret": "s"},
	})
	require.NoError(t, err)

	var resp struct {
		ID            string `json:"id"`
		RefreshBefore string `json:"refreshBefore"`
	}
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	assert.Equal(t, "rb-test", resp.ID)
	assert.NotEmpty(t, resp.RefreshBefore)

	rb, err := time.Parse(time.RFC3339, resp.RefreshBefore)
	require.NoError(t, err)
	assert.True(t, rb.After(time.Now()))
}

// TestWebhookKeyedByURLAndID verifies (url, id) composite keying.
func TestWebhookKeyedByURLAndID(t *testing.T) {
	webhooks := events.NewWebhookRegistry()
	webhooks.Register("sub-1", "http://example.com/hook", "secret-1")
	webhooks.Register("sub-2", "http://example.com/hook", "secret-2")

	targets := webhooks.Targets()
	assert.Len(t, targets, 2)

	webhooks.Unregister("http://example.com/hook", "sub-1")
	targets = webhooks.Targets()
	assert.Len(t, targets, 1)
	assert.Equal(t, "sub-2", targets[0].ID)
}

// TestWebhookTTLExpiry verifies TTL-based expiry.
func TestWebhookTTLExpiry(t *testing.T) {
	webhooks := events.NewWebhookRegistry()
	webhooks.Register("exp-test", "http://example.com/hook", "secret")
	assert.Len(t, webhooks.Targets(), 1)

	webhooks.ExpireAll() // test helper
	assert.Empty(t, webhooks.Targets())
}

// TestWebhookRetryOnServerError verifies retry with backoff on 5xx.
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
	webhooks.Register("retry-test", srv.URL, "secret")

	event := events.MakeEvent("telegram.message", "evt_retry", "1", time.Now(),
		map[string]string{"text": "retry me"})
	webhooks.Deliver(event)

	time.Sleep(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, attempts)
}

// TestWebhookNoRetryOn4xx verifies 4xx errors are not retried.
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
	webhooks.Register("no-retry", srv.URL, "secret")

	event := events.MakeEvent("telegram.message", "evt_4xx", "1", time.Now(),
		map[string]string{"text": "no retry"})
	webhooks.Deliver(event)

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, attempts)
}

// TestVerifySignature verifies the HMAC verification helper.
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
