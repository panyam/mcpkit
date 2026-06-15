package events

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for issue 764 — failure-based GC of no-expiry subscriptions.
// Spec PR1 commit 99f3589c §"Subscription TTL": a server granting
// no-expiry MAY drop the subscription after sustained delivery
// failure (server-defined window) and SHOULD attempt a `terminated`
// envelope.
//
// State-machine layering verified here:
//
//   - TTL prune (existing, exempted for no-expiry) — covered by
//     ttl_negotiation_test.go::TestPruneLoop_SkipsNoExpiryTargets
//   - Suspend (existing, reversible on refresh) — covered by
//     delivery_status_test.go::TestSuspend_*
//   - Failure-based GC (new, no-expiry only, irreversible) — covered
//     here.

// gcTestKey constructs a canonical key consistent with the real
// subscribe path so the tests exercise the same identity surface.
func gcTestKey(t *testing.T, principal, eventName, url string) []byte {
	t.Helper()
	return canonicalKey(principal, url, eventName, nil)
}

// registerNoExpiryTarget shoves a no-expiry target straight into the
// registry, bypassing the events/subscribe handler so the test fixture
// stays focused on the failure-GC logic. Mirrors what the handler does
// when WithAllowInfiniteWebhookTTL is enabled and the client sends
// ttlMs: null.
func registerNoExpiryTarget(t *testing.T, r *WebhookRegistry, principal, eventName, url string) []byte {
	t.Helper()
	key := gcTestKey(t, principal, eventName, url)
	_, _ = r.Register(RegisterParams{
		CanonicalKey: key,
		DerivedID:    "sub_" + principal + "_" + eventName,
		URL:          url,
		Secret:       "whsec_" + strings.Repeat("a", 32),
		EventName:    eventName,
		Principal:    principal,
		NoExpiry:     true,
	})
	return key
}

// TestFailureGC_NoExpiry_DropsAfterContinuousFailureWindow — past the
// configured window of unbroken failure, the registry deletes the
// subscription entirely AND posts a `terminated` envelope. This is
// the spec's "MAY drop after sustained delivery failure" path.
func TestFailureGC_NoExpiry_DropsAfterContinuousFailureWindow(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithAllowInfiniteWebhookTTL(),
		WithNoExpiryFailureGCWindow(50*time.Millisecond),
	)

	var capturedPath atomic.Pointer[string]
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Capture the webhook-id header so we can verify the GC posted
		// a `terminated` envelope (vs a normal event delivery).
		id := req.Header.Get("webhook-id")
		if strings.HasPrefix(id, "msg_terminated_") {
			s := id
			capturedPath.Store(&s)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer recv.Close()

	key := registerNoExpiryTarget(t, r, "alice", "fake.event", recv.URL)

	// First failure anchors FailingContinuouslySince; verify it's set.
	r.recordDeliveryFailure(key, DeliveryError5xx)
	target, found := r.lookupTarget(key)
	require.True(t, found)
	require.NotNil(t, target.Status.FailingContinuouslySince,
		"first failure MUST anchor FailingContinuouslySince")

	// Wait past the GC window.
	time.Sleep(60 * time.Millisecond)

	// Next failure crosses the window → drop + terminated envelope.
	r.recordDeliveryFailure(key, DeliveryError5xx)

	_, stillThere := r.lookupTarget(key)
	assert.False(t, stillThere,
		"no-expiry sub past GC window MUST be removed from the registry; got found=%v", stillThere)

	// Allow the async terminated envelope to land at the receiver.
	require.Eventually(t, func() bool { return capturedPath.Load() != nil },
		2*time.Second, 20*time.Millisecond,
		"failure-GC drop MUST POST a `terminated` envelope (webhook-id prefix msg_terminated_)")
}

// TestFailureGC_NoExpiry_DoesNotDropWhileWindowNotElapsed — repeated
// failures inside the GC window keep the subscription alive (it gets
// suspended by the existing suspend-threshold path, but not dropped).
func TestFailureGC_NoExpiry_DoesNotDropWhileWindowNotElapsed(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithAllowInfiniteWebhookTTL(),
		WithNoExpiryFailureGCWindow(10*time.Second), // not going to elapse during this test
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer recv.Close()

	key := registerNoExpiryTarget(t, r, "alice", "fake.event", recv.URL)

	// Pile on failures rapidly — well within the 10s GC window.
	for i := 0; i < 10; i++ {
		r.recordDeliveryFailure(key, DeliveryError5xx)
	}
	target, found := r.lookupTarget(key)
	require.True(t, found, "no-expiry sub MUST remain in the registry while inside the GC window")
	// Suspend threshold (default 5) is well exceeded; it will have flipped Active=false.
	assert.False(t, target.Status.Active, "consecutive failures exceeded suspendThreshold → Active=false")
	require.NotNil(t, target.Status.FailingContinuouslySince)
	assert.True(t, time.Since(*target.Status.FailingContinuouslySince) < 10*time.Second,
		"FailingContinuouslySince MUST still be inside the GC window")
}

// TestFailureGC_FiniteSub_NotAffectedByGCPath — a finite-TTL sub
// failing past what would be the no-expiry GC window does NOT get
// dropped. Its backstop is TTL expiry, not failure-based GC.
func TestFailureGC_FiniteSub_NotAffectedByGCPath(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithNoExpiryFailureGCWindow(50*time.Millisecond),
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer recv.Close()

	// Finite-TTL sub: explicit ExpiresAtOverride well in the future.
	farFuture := time.Now().Add(1 * time.Hour)
	key := gcTestKey(t, "alice", "fake.event", recv.URL)
	_, _ = r.Register(RegisterParams{
		CanonicalKey:      key,
		DerivedID:         "sub_finite",
		URL:               recv.URL,
		Secret:            "whsec_" + strings.Repeat("a", 32),
		EventName:         "fake.event",
		Principal:         "alice",
		ExpiresAtOverride: &farFuture,
	})

	r.recordDeliveryFailure(key, DeliveryError5xx)
	time.Sleep(60 * time.Millisecond)
	r.recordDeliveryFailure(key, DeliveryError5xx)

	target, found := r.lookupTarget(key)
	assert.True(t, found,
		"finite-TTL sub MUST NOT be dropped by the failure-GC path even when the no-expiry GC window has elapsed; got found=%v", found)
	// FailingContinuouslySince is still maintained on finite subs as
	// diagnostic information — the GC trigger just skips them.
	require.NotNil(t, target.Status.FailingContinuouslySince)
}

// TestFailingContinuouslySince_ClearedOnSuccess — a successful
// delivery resets the GC clock so the next failure starts a fresh
// continuous run.
func TestFailingContinuouslySince_ClearedOnSuccess(t *testing.T) {
	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	key := gcTestKey(t, "alice", "fake.event", recv.URL)
	_, _ = r.Register(RegisterParams{
		CanonicalKey: key, DerivedID: "sub_ok", URL: recv.URL,
		Secret: "whsec_" + strings.Repeat("a", 32),
		EventName: "fake.event", Principal: "alice",
	})

	r.recordDeliveryFailure(key, DeliveryError5xx)
	target, _ := r.lookupTarget(key)
	require.NotNil(t, target.Status.FailingContinuouslySince,
		"failure should anchor the clock")

	r.recordDeliverySuccess(key, time.Now())
	target, _ = r.lookupTarget(key)
	assert.Nil(t, target.Status.FailingContinuouslySince,
		"successful delivery MUST clear FailingContinuouslySince")
}

// TestFailingContinuouslySince_NotResetBySlidingWindow — contrasts
// with FailedSince. The sliding suspendWindow resets FailedSince
// (existing behavior); FailingContinuouslySince stays anchored across
// the same gap because the GC trigger needs "really been failing"
// not "failing within a recent window."
func TestFailingContinuouslySince_NotResetBySlidingWindow(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookSuspendWindow(50*time.Millisecond),
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer recv.Close()

	key := gcTestKey(t, "alice", "fake.event", recv.URL)
	_, _ = r.Register(RegisterParams{
		CanonicalKey: key, DerivedID: "sub_window", URL: recv.URL,
		Secret: "whsec_" + strings.Repeat("a", 32),
		EventName: "fake.event", Principal: "alice",
	})

	r.recordDeliveryFailure(key, DeliveryError5xx)
	target, _ := r.lookupTarget(key)
	require.NotNil(t, target.Status.FailedSince)
	require.NotNil(t, target.Status.FailingContinuouslySince)
	firstAnchor := *target.Status.FailingContinuouslySince

	// Wait past the suspendWindow; the next failure resets FailedSince
	// per the existing sliding-window logic.
	time.Sleep(60 * time.Millisecond)
	r.recordDeliveryFailure(key, DeliveryError5xx)

	target, _ = r.lookupTarget(key)
	require.NotNil(t, target.Status.FailingContinuouslySince)
	assert.True(t, target.Status.FailingContinuouslySince.Equal(firstAnchor),
		"FailingContinuouslySince MUST be preserved across sliding-window resets; got %v want %v",
		target.Status.FailingContinuouslySince, firstAnchor)

	// FailedSince was reset by the sliding-window logic.
	require.NotNil(t, target.Status.FailedSince)
	assert.False(t, target.Status.FailedSince.Equal(firstAnchor),
		"FailedSince SHOULD have been reset by the sliding window")
}

// TestDeliveryStatus_FailingContinuouslySinceProjected — the
// diagnostic field appears on the wire when set, omitted when nil.
func TestDeliveryStatus_FailingContinuouslySinceProjected(t *testing.T) {
	anchor := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	out, ok := deliveryStatusForResponse(DeliveryStatus{
		Active:                   true,
		FailedSince:              &anchor,
		FailingContinuouslySince: &anchor,
	})
	require.True(t, ok)
	assert.Equal(t, anchor.Format(time.RFC3339), out["failingContinuouslySince"])

	// Absent when nil.
	out2, ok2 := deliveryStatusForResponse(DeliveryStatus{
		Active:      true,
		FailedSince: &anchor,
	})
	require.True(t, ok2)
	_, present := out2["failingContinuouslySince"]
	assert.False(t, present,
		"failingContinuouslySince MUST be omitted when nil; got %v", out2["failingContinuouslySince"])
}

// TestConstructorWarning_InfiniteTTLWithInMemoryStore — flipping
// WithAllowInfiniteWebhookTTL without replacing the default
// in-memory store fires a stark warning. Mirrors the
// WithUnsafeWebhookTTLBypass startup-warning posture.
func TestConstructorWarning_InfiniteTTLWithInMemoryStore(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, format)
	}

	// Construct with infinite-TTL but no explicit WebhookStore.
	r := NewWebhookRegistry(WithAllowInfiniteWebhookTTL())
	r.setLogfForTest(logf) // log capture happens after construction; manually re-run the check.
	r.warnIfInfiniteTTLWithDefaultStore()

	require.NotEmpty(t, logs)
	joined := strings.Join(logs, "\n")
	assert.Contains(t, joined, "WithAllowInfiniteWebhookTTL")
	assert.Contains(t, joined, "in-memory")
	assert.Contains(t, joined, "WithWebhookStore",
		"warning MUST point operators at the recommended fix")
}

// TestConstructorWarning_FiniteTTLDoesNotWarn — operator who didn't
// opt into infinite-TTL gets no warning even with the in-memory store.
func TestConstructorWarning_FiniteTTLDoesNotWarn(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, format)
	}
	r := NewWebhookRegistry()
	r.setLogfForTest(logf)
	r.warnIfInfiniteTTLWithDefaultStore()
	assert.Empty(t, logs, "default registry (no infinite-TTL opt-in) MUST NOT emit a no-expiry warning")
}
