package events

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// withFastRetries shortens the delivery retry backoff from 500ms to 1ms, so
// a test that drives failed deliveries waits milliseconds rather than 3.5s per
// delivery. The retry count and the doubling stay as in production.
func withFastRetries() WebhookOption {
	return func(r *WebhookRegistry) { r.retryBackoff = time.Millisecond }
}

// waitForLastError waits until the target under canonical records a delivery
// error, which deliver does only once its retries are exhausted.
func waitForLastError(t *testing.T, r *WebhookRegistry, canonical []byte) {
	t.Helper()
	require.Eventually(t, func() bool {
		return r.DeliveryStatus(canonical).LastError != DeliveryErrorNone
	}, 5*time.Second, 5*time.Millisecond, "delivery never recorded a final error")
}
