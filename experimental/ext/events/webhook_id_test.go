package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEP-3415 at b0efd116 (§"Signature scheme"): webhook-id identifies the
// delivered message, not the event. The server generates it, it is unique
// per (event, subscription), reused across retries of that message, header
// safe, and MUST NOT be derived from eventId. One upstream event matching two
// subscriptions that share a receiver must arrive with two different ids,
// or a receiver deduplicating on webhook-id drops the second delivery.

var webhookIDSafe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type webhookIDCapture struct {
	mu    sync.Mutex
	ids   []string
	fails int // respond 500 to this many requests first
}

func (c *webhookIDCapture) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.ids = append(c.ids, r.Header.Get("webhook-id"))
		if c.fails > 0 {
			c.fails--
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (c *webhookIDCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

func TestWebhookID_PerMessageNotEventID(t *testing.T) {
	cap := &webhookIDCapture{}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks())
	secret := "whsec_" + strings.Repeat("a", 32)
	keyA := canonicalKey("alice", srv.URL, "chat.message", map[string]any{"channel": "general"})
	keyB := canonicalKey("alice", srv.URL, "chat.message", map[string]any{"from": "bob"})
	r.Register(RegisterParams{CanonicalKey: keyA, DerivedID: deriveSubscriptionID(keyA), URL: srv.URL, Secret: secret})
	r.Register(RegisterParams{CanonicalKey: keyB, DerivedID: deriveSubscriptionID(keyB), URL: srv.URL, Secret: secret})

	ev := Event{EventID: "evt.upstream.42", Name: "chat.message", Timestamp: time.Now().UTC().Format(time.RFC3339), Data: json.RawMessage(`{}`)}
	require.True(t, r.DeliverToTarget(context.Background(), keyA, ev))
	require.True(t, r.DeliverToTarget(context.Background(), keyB, ev))

	require.Eventually(t, func() bool { return len(cap.snapshot()) == 2 }, 3*time.Second, 20*time.Millisecond)
	ids := cap.snapshot()

	for _, id := range ids {
		assert.NotEqual(t, ev.EventID, id, "webhook-id MUST NOT be derived from eventId")
		assert.Regexp(t, webhookIDSafe, id, "webhook-id must be ASCII letters, digits, _ and -")
	}
	assert.NotEqual(t, ids[0], ids[1],
		"one event on two subscriptions is two messages and needs two webhook-ids")
}

func TestWebhookID_ReusedAcrossRetries(t *testing.T) {
	cap := &webhookIDCapture{fails: 1}
	srv := httptest.NewServer(cap.handler())
	defer srv.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks())
	key := canonicalKey("alice", srv.URL, "chat.message", nil)
	r.Register(RegisterParams{CanonicalKey: key, DerivedID: deriveSubscriptionID(key), URL: srv.URL, Secret: "whsec_" + strings.Repeat("a", 32)})

	ev := Event{EventID: "evt.upstream.43", Name: "chat.message", Timestamp: time.Now().UTC().Format(time.RFC3339), Data: json.RawMessage(`{}`)}
	require.True(t, r.DeliverToTarget(context.Background(), key, ev))

	require.Eventually(t, func() bool { return len(cap.snapshot()) == 2 }, 5*time.Second, 20*time.Millisecond)
	ids := cap.snapshot()
	assert.Equal(t, ids[0], ids[1], "a retry keeps the webhook-id of the message it retries")
	assert.NotEqual(t, ev.EventID, ids[0], "webhook-id MUST NOT be derived from eventId")
}
