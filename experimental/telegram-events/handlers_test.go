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
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
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

// newConnectedClient creates a fully wired MCP server (tools + methods + resources),
// starts it on httptest, connects a client, and returns both.
func newConnectedClient(t *testing.T, store *MessageStore, webhooks *WebhookRegistry) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := server.NewServer(
		core.ServerInfo{Name: "telegram-events-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	registerResources(srv, store)
	(&ToolDelivery{Bot: nil}).Register(srv, store, webhooks)
	(&MethodDelivery{}).Register(srv, store, webhooks)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	return c, ts
}

// pollResult is the structure returned by events/poll for a single subscription.
type pollResult struct {
	ID     string          `json:"id"`
	Events []TelegramEvent `json:"events"`
	Cursor string          `json:"cursor"`
}

// TestEventsPollCursorPagination verifies that the events/poll method returns
// events after the cursor and provides the correct next cursor for
// subsequent polls.
func TestEventsPollCursorPagination(t *testing.T) {
	store := newTestStore(10)
	c, _ := newConnectedClient(t, store, NewWebhookRegistry())

	// First poll: cursor=0, maxEvents=5 → events 1-5, cursor="5"
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
	assert.Len(t, resp.Results[0].Events, 5, "first page should have 5 events")
	assert.Equal(t, "5", resp.Results[0].Cursor, "cursor should be 5")
	assert.Equal(t, "message 1", resp.Results[0].Events[0].Data.Text)
	assert.Equal(t, "message 5", resp.Results[0].Events[4].Data.Text)

	// Second poll: cursor=5, maxEvents=5 → events 6-10, cursor="10"
	result2, err := c.Call("events/poll", map[string]any{
		"maxEvents": 5,
		"subscriptions": []map[string]any{
			{"id": "page2", "name": "telegram.message", "cursor": resp.Results[0].Cursor},
		},
	})
	require.NoError(t, err)

	var resp2 struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result2.Raw, &resp2))
	assert.Len(t, resp2.Results[0].Events, 5, "second page should have 5 events")
	assert.Equal(t, "10", resp2.Results[0].Cursor, "cursor should be 10")
	assert.Equal(t, "message 6", resp2.Results[0].Events[0].Data.Text)
	assert.Equal(t, "message 10", resp2.Results[0].Events[4].Data.Text)
}

// TestEventsPollEmptyStore verifies that polling an empty store returns an
// empty events list and cursor "0".
func TestEventsPollEmptyStore(t *testing.T) {
	store := NewMessageStore(1000)
	c, _ := newConnectedClient(t, store, NewWebhookRegistry())

	result, err := c.Call("events/poll", map[string]any{
		"subscriptions": []map[string]any{
			{"id": "empty", "name": "telegram.message", "cursor": "0"},
		},
	})
	require.NoError(t, err)

	var resp struct{ Results []pollResult }
	require.NoError(t, json.Unmarshal(result.Raw, &resp))
	require.Len(t, resp.Results, 1)
	assert.Empty(t, resp.Results[0].Events, "empty store should return no events")
	assert.Equal(t, "0", resp.Results[0].Cursor, "cursor should remain 0")
}

// TestEventsPollUnknownEvent verifies that polling for an unknown event name
// returns an error for that subscription without failing the request.
func TestEventsPollUnknownEvent(t *testing.T) {
	store := NewMessageStore(1000)
	c, _ := newConnectedClient(t, store, NewWebhookRegistry())

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
	require.NotNil(t, resp.Results[0].Error, "unknown event should return error")
	assert.Equal(t, -32001, resp.Results[0].Error.Code)
	assert.Equal(t, "EventNotFound", resp.Results[0].Error.Message)
}

// TestResourceRecentMessages verifies that the telegram://messages/recent
// resource returns stored messages as JSON.
func TestResourceRecentMessages(t *testing.T) {
	store := newTestStore(3)
	c, _ := newConnectedClient(t, store, NewWebhookRegistry())

	text, err := c.ReadResource("telegram://messages/recent")
	require.NoError(t, err)

	var msgs []Message
	require.NoError(t, json.Unmarshal([]byte(text), &msgs))
	assert.Len(t, msgs, 3, "should return all 3 messages")
	assert.Equal(t, "message 1", msgs[0].Text)
}

// TestResourceMessageByID verifies that the telegram://message/{id} template
// resource returns the correct message.
func TestResourceMessageByID(t *testing.T) {
	store := newTestStore(3)
	c, _ := newConnectedClient(t, store, NewWebhookRegistry())

	text, err := c.ReadResource("telegram://message/2")
	require.NoError(t, err)

	var msg Message
	require.NoError(t, json.Unmarshal([]byte(text), &msg))
	assert.Equal(t, int64(2), msg.ID)
	assert.Equal(t, "message 2", msg.Text)
}

// TestWebhookHMACSignature verifies that outbound webhook deliveries include
// a correct HMAC-SHA256 signature in the X-Signature-256 header.
func TestWebhookHMACSignature(t *testing.T) {
	secret := "test-secret-key"
	var receivedBody []byte
	var receivedSig string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Signature-256")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	webhooks := NewWebhookRegistry()
	webhooks.Register(ts.URL, secret)

	event := TelegramEvent{
		EventID:   "evt_1",
		Name:      "telegram.message",
		Timestamp: "2024-01-01T00:00:00Z",
		Data:      TelegramEventData{ChatID: "100", User: "test", Text: "hello"},
		Cursor:    "1",
	}
	webhooks.Deliver(event)

	time.Sleep(100 * time.Millisecond)

	require.NotEmpty(t, receivedBody, "webhook should have received a POST")
	require.NotEmpty(t, receivedSig, "webhook should include X-Signature-256 header")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expectedSig := fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
	assert.Equal(t, expectedSig, receivedSig, "HMAC signature should match")
}

// TestMessageStoreCursorSemantics verifies the cursor-based retrieval contract:
// GetSince returns only messages with ID strictly greater than the cursor.
func TestMessageStoreCursorSemantics(t *testing.T) {
	store := NewMessageStore(100)
	store.Add(1, "alice", "first", time.Now())
	store.Add(1, "bob", "second", time.Now())
	store.Add(1, "carol", "third", time.Now())

	msgs, next := store.GetSince(0, 100)
	assert.Len(t, msgs, 3)
	assert.Equal(t, int64(3), next)

	msgs, next = store.GetSince(1, 100)
	assert.Len(t, msgs, 2)
	assert.Equal(t, "second", msgs[0].Text)
	assert.Equal(t, int64(3), next)

	msgs, next = store.GetSince(3, 100)
	assert.Empty(t, msgs)
	assert.Equal(t, int64(3), next)
}

// TestMessageStoreRingBuffer verifies that the store evicts old messages
// when the buffer exceeds maxSize.
func TestMessageStoreRingBuffer(t *testing.T) {
	store := NewMessageStore(3)
	for i := 1; i <= 5; i++ {
		store.Add(1, "user", fmt.Sprintf("msg %d", i), time.Now())
	}
	assert.Equal(t, 3, store.Len(), "store should cap at maxSize")
	recent := store.Recent(10)
	assert.Len(t, recent, 3)
	assert.Equal(t, "msg 3", recent[0].Text, "oldest surviving message should be msg 3")
	assert.Equal(t, "msg 5", recent[2].Text)
}

// TestVerifySignature verifies the HMAC verification helper.
func TestVerifySignature(t *testing.T) {
	secret := "my-secret"
	body := []byte(`{"test":"data"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))

	assert.True(t, VerifySignature(body, secret, sig), "valid signature should verify")
	assert.False(t, VerifySignature(body, "wrong-secret", sig), "wrong secret should fail")
	assert.False(t, VerifySignature([]byte("tampered"), secret, sig), "tampered body should fail")
}
