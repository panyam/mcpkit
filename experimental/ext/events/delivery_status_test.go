package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ζ-5 — deliveryStatus on events/subscribe refresh response.
//
// Spec §"Webhook Delivery Status" L425-460: the registry tracks per-
// target delivery health. Subscribe refresh response includes:
//
//   deliveryStatus: {
//     active:         bool,
//     lastDeliveryAt: ISO-8601 string (omitted when never delivered),
//     lastError:      categorical string (omitted when no failure),
//     failedSince:    ISO-8601 string (omitted when last attempt OK)
//   }
//
// lastError is from a FIXED CATEGORICAL SET — never raw response bodies,
// headers, or status lines. The reason: the subscribe response is
// visible to the subscriber. A receiver returning internal information
// in its response body would otherwise leak to whoever subscribed.
// Spec L460: "Servers MUST NOT include raw response data in
// deliveryStatus.lastError."
//
// Allowed categorical values:
//   connection_refused | timeout | tls_error
//   http_3xx_redirect  | http_4xx | http_5xx
//   challenge_failed   | (empty when no failure)
//
// First subscribe (no prior attempts) MUST omit deliveryStatus entirely
// — there's nothing to report and emitting an empty one would just
// add bytes to every initial subscribe response.

// dispatchSubscribeForStatus is a small helper that drives an
// events/subscribe call from a test, returning the parsed response
// result. The caller has already registered + driven deliveries
// against the target so the refresh path has something to report.
func dispatchSubscribeForStatus(t *testing.T, srv *server.Server, callbackURL, secret string) map[string]any {
	t.Helper()
	params := map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    callbackURL,
			"secret": secret,
		},
	}
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/subscribe", Params: raw,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "expected success; got %+v", resp.Error)

	body, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	return m
}

// TestDeliveryStatus_OmittedOnFirstSubscribe verifies the contract that
// the subscribe response carries NO deliveryStatus on the initial
// subscribe — no attempts have happened, nothing to report. Adding an
// empty status object would just be wire bloat.
func TestDeliveryStatus_OmittedOnFirstSubscribe(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	resp := dispatchSubscribe(t, srv, validSubscribeParams())
	require.Nil(t, resp.Error)

	body, _ := json.Marshal(resp.Result)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	_, has := m["deliveryStatus"]
	assert.False(t, has,
		"first subscribe MUST omit deliveryStatus (no prior attempts to report); got %s", string(body))
}

// TestDeliveryStatus_AfterSuccessPopulatesLastDeliveryAt verifies that
// after a successful delivery, the next subscribe refresh response
// includes deliveryStatus.lastDeliveryAt (ISO-8601). active=true,
// lastError empty/absent, failedSince absent.
func TestDeliveryStatus_AfterSuccessPopulatesLastDeliveryAt(t *testing.T) {
	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	srv, webhooks := buildAuthGateStack(t, "test-principal")

	// Initial subscribe (won't have status; that's the prior test).
	params := map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	}
	require.Nil(t, dispatchSubscribe(t, srv, params).Error)

	// Drive one successful delivery.
	webhooks.Deliver(MakeEvent("fake.event", "evt_1", "1", time.Now(), map[string]string{"k": "v"}))
	require.Eventually(t, func() bool { return hits.Load() >= 1 },
		2*time.Second, 20*time.Millisecond, "delivery should have landed")

	// Refresh subscribe — same tuple, idempotent. Response should now
	// include deliveryStatus.
	result := dispatchSubscribeForStatus(t, srv, receiver.URL, params["delivery"].(map[string]any)["secret"].(string))
	status, ok := result["deliveryStatus"].(map[string]any)
	require.True(t, ok, "refresh response MUST include deliveryStatus after a delivery; got %+v", result)
	assert.Equal(t, true, status["active"])
	assert.NotEmpty(t, status["lastDeliveryAt"], "lastDeliveryAt MUST be populated after a delivery")
	_, hasErr := status["lastError"]
	assert.False(t, hasErr, "lastError MUST be omitted after a clean delivery; got %v", status["lastError"])
}

// TestDeliveryStatus_LastErrorIsCategorical verifies that a delivery
// failure populates lastError with a CATEGORICAL value, not a raw
// substring of the receiver's response body. Spec L460 mandates this
// to avoid the subscribe response becoming a response oracle for
// attacker-chosen URLs.
func TestDeliveryStatus_LastErrorIsCategorical(t *testing.T) {
	// Receiver returns 500 with a body that, if leaked, would tell us.
	const oracleBody = "INTERNAL_SERVER_ORACLE_LEAK"
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(oracleBody))
	}))
	defer receiver.Close()

	srv, webhooks := buildAuthGateStack(t, "test-principal")
	params := map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	}
	require.Nil(t, dispatchSubscribe(t, srv, params).Error)

	webhooks.Deliver(MakeEvent("fake.event", "evt_fail", "1", time.Now(), map[string]string{"k": "v"}))
	// Wait long enough for the deliver loop to retry-and-fail (3 retries
	// with backoff 0.5s + 1s + 2s = ~3.5s total).
	time.Sleep(4 * time.Second)

	result := dispatchSubscribeForStatus(t, srv, receiver.URL, params["delivery"].(map[string]any)["secret"].(string))
	status, ok := result["deliveryStatus"].(map[string]any)
	require.True(t, ok, "refresh after failure MUST report deliveryStatus")

	lastErr, _ := status["lastError"].(string)
	assert.Equal(t, "http_5xx", lastErr,
		"lastError MUST be the categorical bucket; got %q", lastErr)

	// Stronger assertion: the oracle body must NOT appear ANYWHERE in
	// the response. Catches future regressions where someone might
	// accidentally include err.Error() (which Go's http.Client may
	// include parts of).
	body, _ := json.Marshal(result)
	assert.False(t, strings.Contains(string(body), oracleBody),
		"raw receiver response body MUST NOT leak into deliveryStatus; got %s", string(body))
	assert.False(t, strings.Contains(string(body), "INTERNAL_SERVER"),
		"any substring of the receiver body must not leak; got %s", string(body))

	// failedSince should be populated (we have a current run of failures).
	assert.NotEmpty(t, status["failedSince"], "failedSince MUST be set during a current failure run")
}

// TestSuspend_AfterThresholdConsecutiveFailures verifies the spec
// suspend rule (§"Webhook Event Delivery" L413 + §"Webhook Delivery
// Status" L460): after N consecutive delivery failures within a sliding
// window W, the registry sets Active=false on the target. Subsequent
// yields don't attempt delivery (covered by separate test below).
//
// Without this state machine, a permanently-down receiver would
// accumulate retry traffic indefinitely — effectively a self-DoS for
// every subscription pointing at a dead URL.
func TestSuspend_AfterThresholdConsecutiveFailures(t *testing.T) {
	// Receiver always returns 500 — every deliver() ends in failure.
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()

	const threshold = 3
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookSuspendThreshold(threshold),
		WithWebhookSuspendWindow(10*time.Second),
	)
	canonical := []byte("suspend-threshold-test")
	r.Register(canonical, "sub_st", receiver.URL, "whsec_secret", 0)

	// Drive `threshold` consecutive failed deliveries (each deliver()
	// internally retries; we count whole-call outcomes, not retries).
	for i := 0; i < threshold; i++ {
		r.deliver(r.Targets()[0], "evt_"+string(rune('a'+i)), []byte(`{}`))
	}

	st := r.DeliveryStatus(canonical)
	assert.False(t, st.Active,
		"after %d consecutive failures, target MUST be suspended (Active=false); got %+v", threshold, st)
}

// TestSuspend_FailuresOutsideWindowDontAccumulate verifies the sliding-
// window semantic: failures separated by more than W don't count
// toward the same run. A receiver that has one failure per hour for
// weeks shouldn't get suspended; only a *current* run of failures
// does.
//
// Implementation strategy here: use a deliberately tiny window so
// the test runs fast.
func TestSuspend_FailuresOutsideWindowDontAccumulate(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()

	const threshold = 3
	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookSuspendThreshold(threshold),
		WithWebhookSuspendWindow(200*time.Millisecond),
	)
	canonical := []byte("suspend-window-test")
	r.Register(canonical, "sub_sw", receiver.URL, "whsec_secret", 0)

	// 2 failures, then sleep past the window, then 2 more.
	// First-failure-time should reset on the post-sleep failures, so
	// total counted-toward-current-run = 2 < threshold = 3 → still Active.
	r.deliver(r.Targets()[0], "evt_1", []byte(`{}`))
	r.deliver(r.Targets()[0], "evt_2", []byte(`{}`))

	time.Sleep(300 * time.Millisecond) // > 200ms window

	r.deliver(r.Targets()[0], "evt_3", []byte(`{}`))
	r.deliver(r.Targets()[0], "evt_4", []byte(`{}`))

	st := r.DeliveryStatus(canonical)
	assert.True(t, st.Active,
		"failures separated by more than the suspend window MUST NOT accumulate; got %+v", st)
}

// TestSuspend_SuccessfulRefreshReactivates verifies that re-subscribing
// (idempotent refresh — same canonical tuple) flips a suspended target
// back to Active=true, clearing the failure run. Per spec L460: "a
// successful refresh reactivates."
//
// The reactivation path is: client retries subscribe → matches existing
// canonical key → registry refresh path runs → sees Status.Active=false,
// resets to true + clears failure state.
func TestSuspend_SuccessfulRefreshReactivates(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()

	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookSuspendThreshold(2),
		WithWebhookSuspendWindow(10*time.Second),
	)
	canonical := []byte("reactivate-test")
	r.Register(canonical, "sub_react", receiver.URL, "whsec_secret", 0)

	// Drive 2 consecutive failures → suspended.
	r.deliver(r.Targets()[0], "evt_a", []byte(`{}`))
	r.deliver(r.Targets()[0], "evt_b", []byte(`{}`))
	require.False(t, r.DeliveryStatus(canonical).Active, "precondition: target should be suspended")

	// Refresh — same canonical tuple, idempotent re-Register.
	r.Register(canonical, "sub_react", receiver.URL, "whsec_secret", 0)

	st := r.DeliveryStatus(canonical)
	assert.True(t, st.Active, "successful refresh MUST reactivate; got %+v", st)
	assert.Equal(t, DeliveryErrorNone, st.LastError, "refresh MUST clear lastError")
	assert.Nil(t, st.FailedSince, "refresh MUST clear failedSince")
}

// TestSuspend_SuspendedTargetSkippedInDeliver verifies that a suspended
// target is omitted from the broadcast list — yielded events no longer
// attempt delivery to it. Without this skip, the suspend state would
// be cosmetic (just a status flag) and the dead receiver would still
// see retry traffic on every yield.
func TestSuspend_SuspendedTargetSkippedInDeliver(t *testing.T) {
	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()

	r := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookSuspendThreshold(2),
		WithWebhookSuspendWindow(10*time.Second),
	)
	canonical := []byte("skip-deliver-test")
	r.Register(canonical, "sub_skip", receiver.URL, "whsec_secret", 0)

	// Drive 2 failures → suspended.
	r.deliver(r.Targets()[0], "evt_a", []byte(`{}`))
	r.deliver(r.Targets()[0], "evt_b", []byte(`{}`))
	require.False(t, r.DeliveryStatus(canonical).Active)

	// Note hits up to here so we measure only what happens AFTER suspension.
	hitsBeforeSuspendYield := hits.Load()

	// Yield via Deliver — suspended target should be skipped.
	r.Deliver(MakeEvent("fake.event", "evt_post_suspend", "1", time.Now(), map[string]string{"k": "v"}))
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, hitsBeforeSuspendYield, hits.Load(),
		"suspended target MUST NOT receive new delivery attempts; got %d new hits", hits.Load()-hitsBeforeSuspendYield)
}

// TestDeliveryStatus_LastErrorBucketsForConnectionRefused verifies the
// connection_refused bucket — a receiver that's down (refused TCP).
// Use a known-closed port.
func TestDeliveryStatus_LastErrorBucketsForConnectionRefused(t *testing.T) {
	srv, webhooks := buildAuthGateStack(t, "test-principal")
	// Port 1 is reserved and reliably refuses.
	deadURL := "http://127.0.0.1:1/sink"
	params := map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    deadURL,
			"secret": generateSecret(),
		},
	}
	require.Nil(t, dispatchSubscribe(t, srv, params).Error)

	webhooks.Deliver(MakeEvent("fake.event", "evt_refused", "1", time.Now(), map[string]string{"k": "v"}))
	time.Sleep(4 * time.Second) // 3 retries with backoff

	result := dispatchSubscribeForStatus(t, srv, deadURL, params["delivery"].(map[string]any)["secret"].(string))
	status, ok := result["deliveryStatus"].(map[string]any)
	require.True(t, ok)
	lastErr, _ := status["lastError"].(string)
	assert.Equal(t, "connection_refused", lastErr,
		"refused TCP MUST classify as connection_refused; got %q", lastErr)
}
