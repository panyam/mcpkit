package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ζ-3 — body size cap + 413 non-retryable.
//
// Spec §"Webhook Security" → "Delivery profile (for WAF / private-cloud
// deployments)" L487:
//   - Servers SHOULD cap outbound delivery bodies at 256 KiB.
//   - Servers MUST treat a 413 Payload Too Large from the receiver as
//     non-retryable.
//
// Cap mode is REJECT, not TRUNCATE — truncation would corrupt the
// HMAC signature and silently drop event content, both bad. The
// rejected event is logged and skipped (it will never get smaller on
// retry).

// TestDelivery_OversizedEventNotPosted verifies a body that exceeds the
// configured cap is NOT POSTed to the receiver. The cap defaults to 256
// KiB (spec); we set it lower for the test so we can build an oversized
// payload without ballooning test memory.
func TestDelivery_OversizedEventNotPosted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 1 KiB cap so we don't allocate a 256 KiB+ blob in the test process.
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookMaxBodyBytes(1024),
	)
	r.Register([]byte("k"), "sub_test", srv.URL, "whsec_secret", 0)

	// Build a payload that, when JSON-marshaled with the envelope,
	// exceeds 1 KiB. 2 KiB of "A" inside Data is enough.
	bigData, _ := json.Marshal(map[string]string{"text": strings.Repeat("A", 2048)})
	r.Deliver(Event{
		EventID:   "evt_oversize",
		Name:      "fake.event",
		Timestamp: time.Now().Format(time.RFC3339),
		Data:      bigData,
	})

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(0), hits.Load(),
		"oversized payload MUST NOT be POSTed; spec L487 cap is reject-not-truncate (truncation would corrupt HMAC)")
}

// TestDelivery_OversizedEventLogged is a counter-assertion: the rejection
// must be observable in the log so operators know events are being
// dropped (silent drop would be worse than retrying).
func TestDelivery_OversizedEventLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logCap := &captureLog{}
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookMaxBodyBytes(1024),
	)
	logCap.attach(r)
	r.Register([]byte("k"), "sub_test", srv.URL, "whsec_secret", 0)

	bigData, _ := json.Marshal(map[string]string{"text": strings.Repeat("B", 2048)})
	r.Deliver(Event{
		EventID:   "evt_oversize_logged",
		Name:      "fake.event",
		Timestamp: time.Now().Format(time.RFC3339),
		Data:      bigData,
	})

	time.Sleep(100 * time.Millisecond)
	logged := false
	for _, line := range logCap.snapshot() {
		if strings.Contains(line, "oversize") || strings.Contains(line, "exceeds") || strings.Contains(line, "too large") {
			logged = true
			break
		}
	}
	assert.True(t, logged,
		"oversized payload rejection MUST be logged; got:\n%s",
		strings.Join(logCap.snapshot(), "\n"))
}

// TestDelivery_413NotRetried verifies that a 413 Payload Too Large
// response from the receiver is classified as non-retryable per spec
// L487. Without this, a receiver permanently configured to reject our
// payloads would accumulate retry traffic indefinitely (effectively a
// self-DoS).
func TestDelivery_413NotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusRequestEntityTooLarge) // 413
	}))
	defer srv.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	r.Register([]byte("k"), "sub_test", srv.URL, "whsec_secret", 0)

	r.Deliver(MakeEvent("fake.event", "evt_413", "1", time.Now(),
		map[string]string{"text": "tiny"}))

	// Generous wait — if 413 were retried with backoff, we'd see 4 attempts
	// over ~3.5 seconds. 1 second covers a single attempt + safety margin.
	time.Sleep(1 * time.Second)

	assert.Equal(t, int32(1), hits.Load(),
		"413 MUST be treated as non-retryable per spec L487; got %d attempts", hits.Load())
}

// TestDelivery_DefaultCapIs256KiB pins the default cap value to 256 KiB
// per spec. Catches an accidental config drift.
func TestDelivery_DefaultCapIs256KiB(t *testing.T) {
	r := NewWebhookRegistry()
	require.Equal(t, 256*1024, r.maxBodyBytes,
		"default body cap MUST be 256 KiB per spec L487")
}
