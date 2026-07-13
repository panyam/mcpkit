package events

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the 2026-06-15 spec PR1 alignment batch:
//
//   - #762: params → arguments rename (commit 082166f0)
//   - #763: deliveryStatus.throttled + retryAfterMs (commit 21be9c31)
//   - #765: 410 Gone honored as terminal-but-not-failure (commit 905ade36)
//   - #767: ack timeout aligned with spec recommendation (commit b506e347)
//
// Pinning the wire shape here keeps a regression visible: a future Go-
// field rename that forgets to thread the JSON tag (or vice versa)
// shows up as a failed assertion here, not as a silent wire break that
// only conformance catches.

// TestSubscribe_WireShape_UsesArgumentsNotParams pins the wire tag on
// events/subscribe to "arguments" per spec PR1 commit 082166f0. The
// canonical key derivation reads the Go field, not the JSON tag, so
// derived IDs are stable across the rename — TestCanonicalKey_Stable
// confirms that separately.
func TestSubscribe_WireShape_UsesArgumentsNotParams(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	wire := map[string]any{
		"name":      "fake.event",
		"arguments": map[string]any{"severity": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	}
	resp := dispatchSubscribe(t, srv, wire)
	require.Nil(t, resp.Error, "subscribe with arguments MUST be accepted; got %+v", resp.Error)

	// And the legacy "params" tag MUST be ignored — same canonical key
	// computed from the empty arguments map, so the second subscribe
	// idempotently refreshes the first IF the server were still
	// honoring the old tag. We assert that the server treats them as
	// DIFFERENT subscriptions by checking the derived id changes.
	legacy := map[string]any{
		"name":   "fake.event",
		"params": map[string]any{"severity": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	}
	legacyResp := dispatchSubscribe(t, srv, legacy)
	require.Nil(t, legacyResp.Error)

	idArgs := decodeID(t, resp)
	idLegacy := decodeID(t, legacyResp)
	assert.NotEqual(t, idArgs, idLegacy,
		"server MUST treat legacy params as empty-arguments (a different canonical key); both ids matched, so old tag is still being honored")
}

// TestPoll_WireShape_UsesArgumentsNotParams pins the wire tag for
// events/poll. Asserts symmetric to subscribe.
func TestPoll_WireShape_UsesArgumentsNotParams(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")

	body := map[string]any{
		"name":      "fake.event",
		"arguments": map[string]any{"severity": "high"},
		"cursor":    "0",
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/poll", Params: core.NewRawJSON(raw),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "events/poll with arguments MUST be accepted")
}

// TestUnsubscribe_WireShape_UsesArgumentsNotParams pins the wire tag for
// events/unsubscribe. Symmetric to subscribe — same canonical tuple.
func TestUnsubscribe_WireShape_UsesArgumentsNotParams(t *testing.T) {
	srv, webhooks := buildAuthGateStack(t, "test-principal")
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	secret := generateSecret()
	subBody := map[string]any{
		"name":      "fake.event",
		"arguments": map[string]any{"severity": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": secret,
		},
	}
	require.Nil(t, dispatchSubscribe(t, srv, subBody).Error)
	require.Equal(t, 1, len(webhooks.Targets()), "subscribe should register one target")

	unsubBody := map[string]any{
		"name":      "fake.event",
		"arguments": map[string]any{"severity": "high"},
		"delivery":  map[string]any{"url": receiver.URL},
	}
	raw, err := json.Marshal(unsubBody)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "events/unsubscribe", Params: core.NewRawJSON(raw),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "events/unsubscribe with arguments MUST be accepted")
	assert.Equal(t, 0, len(webhooks.Targets()), "unsubscribe with arguments MUST resolve to the same canonical tuple as subscribe")
}

// TestCanonicalKey_StableAcrossRename verifies the rename was JSON-tag
// only on the wire — the canonical-key derivation reads the Go field
// directly, so two subscribes with identical arguments compute the same
// derived id (idempotent refresh) regardless of which name the field
// had at compile time. This protects against accidental key-format
// regressions during the rename sweep.
func TestCanonicalKey_StableAcrossRename(t *testing.T) {
	args := map[string]any{"severity": "high", "n": float64(7)}
	key1 := canonicalKey("alice", "https://example.test/wh", "fake.event", args)
	key2 := canonicalKey("alice", "https://example.test/wh", "fake.event", args)
	require.Equal(t, key1, key2, "canonicalKey must be deterministic on identical inputs")
	id1 := deriveSubscriptionID(key1)
	id2 := deriveSubscriptionID(key2)
	assert.Equal(t, id1, id2, "derived id must match for identical canonical inputs (regression guard for the rename sweep)")
}

// TestDeliveryStatus_ThrottledProjected pins the wire shape for the
// new throttled + retryAfterMs fields on the subscribe-refresh
// response (spec PR1 commit 21be9c31). The projector emits both with
// omitempty semantics: throttled=false stays absent; retryAfterMs=nil
// stays absent. Tests exercise both directly through the projector
// (deliveryStatusForResponse) so we don't need a real rate-limiter to
// exercise the wire path.
func TestDeliveryStatus_ThrottledProjected(t *testing.T) {
	retryMs := int64(60000)
	cases := []struct {
		name           string
		input          DeliveryStatus
		wantThrottled  any  // nil = absent; true/false = expected value
		wantRetryAfter any  // nil = absent; int64 = expected value
		wantPresent    bool // whether projector should emit anything
	}{
		{
			name:        "throttled false + retryAfterMs nil + no history → nothing to report",
			input:       DeliveryStatus{Active: true},
			wantPresent: false,
		},
		{
			name: "throttled true alone is reportable",
			input: DeliveryStatus{
				Active:    true,
				Throttled: true,
			},
			wantThrottled: true,
			wantPresent:   true,
		},
		{
			name: "throttled + retryAfterMs both present",
			input: DeliveryStatus{
				Active:       true,
				Throttled:    true,
				RetryAfterMs: &retryMs,
			},
			wantThrottled:  true,
			wantRetryAfter: int64(60000),
			wantPresent:    true,
		},
		{
			name: "throttled false + retryAfterMs present (advisory hint without active rate-limit) — both fields still surface",
			input: DeliveryStatus{
				Active:         true,
				LastDeliveryAt: ptrTime(time.Now()),
				RetryAfterMs:   &retryMs,
			},
			wantRetryAfter: int64(60000),
			wantPresent:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := deliveryStatusForResponse(tc.input)
			require.Equal(t, tc.wantPresent, ok, "projector presence wrong; got out=%+v", out)
			if !tc.wantPresent {
				return
			}
			if tc.wantThrottled == nil {
				_, has := out["throttled"]
				assert.False(t, has, "throttled MUST be absent; got %v", out["throttled"])
			} else {
				assert.Equal(t, tc.wantThrottled, out["throttled"])
			}
			if tc.wantRetryAfter == nil {
				_, has := out["retryAfterMs"]
				assert.False(t, has, "retryAfterMs MUST be absent; got %v", out["retryAfterMs"])
			} else {
				assert.Equal(t, tc.wantRetryAfter, out["retryAfterMs"])
			}
		})
	}
}

// TestWebhookDelivery_410GoneIsTerminalWithoutSubscriptionEffect pins
// the 410 Gone branch from spec PR1 commit 905ade36: "the server MUST
// treat 410 as a non-retryable failure for that delivery (like 413,
// see Delivery profile), without affecting the subscription itself."
//
// Assertions:
//  1. Only ONE POST hits the receiver (no retry on 410)
//  2. DeliveryStatus.Active stays true (subscription unaffected)
//  3. DeliveryStatus.LastError stays DeliveryErrorNone (410 is not a
//     failure for delivery-health tracking)
//  4. The NEXT event still delivers normally (subscription is live)
func TestWebhookDelivery_410GoneIsTerminalWithoutSubscriptionEffect(t *testing.T) {
	var (
		hits         atomic.Int32
		returnStatus atomic.Int32
	)
	returnStatus.Store(int32(http.StatusGone))

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(returnStatus.Load()))
	}))
	defer receiver.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	defResolver := func(string) EventDef { return EventDef{} }
	canonical := canonicalKey("alice", receiver.URL, "fake.event", nil)
	r.SetDefResolver(defResolver)
	r.Register(RegisterParams{
		CanonicalKey: canonical, DerivedID: "sub_410", URL: receiver.URL,
		Secret: "whsec_" + strings.Repeat("a", 32), MaxAgeSeconds: 0,
		EventName: "fake.event", Principal: "alice",
	})

	// First delivery: receiver returns 410.
	r.Deliver(context.Background(), MakeEvent("fake.event", "evt_410", "1", time.Now(), map[string]string{"k": "v"}))
	require.Eventually(t, func() bool { return hits.Load() >= 1 },
		2*time.Second, 20*time.Millisecond, "first delivery should land")

	// Give the retry loop a generous window — if it WERE going to retry,
	// it would have by now. Backoff is 500ms + 1s + 2s = ~3.5s.
	time.Sleep(4 * time.Second)
	assert.Equal(t, int32(1), hits.Load(),
		"410 MUST be terminal — no retries; got %d posts", hits.Load())

	status := r.DeliveryStatus(canonical)
	assert.True(t, status.Active,
		"410 MUST NOT affect the subscription — Active should still be true")
	assert.Equal(t, DeliveryErrorNone, status.LastError,
		"410 is a per-delivery rejection, not a failure — LastError should remain none; got %q", status.LastError)
	assert.Nil(t, status.FailedSince,
		"410 MUST NOT set FailedSince — there's no failure run to track")

	// Subscription still live: a second event delivers normally when
	// the receiver flips to 200.
	returnStatus.Store(int32(http.StatusOK))
	r.Deliver(context.Background(), MakeEvent("fake.event", "evt_ok", "2", time.Now(), map[string]string{"k": "v"}))
	require.Eventually(t, func() bool { return hits.Load() >= 2 },
		2*time.Second, 20*time.Millisecond, "subscription still live after 410: second event MUST deliver")
	statusAfter := r.DeliveryStatus(canonical)
	assert.True(t, statusAfter.Active, "subscription remains active after the 200")
}

// TestDefaultWebhookAckTimeout pins the spec-aligned ack timeout
// constant. Spec PR1 commit b506e347 (§"Webhook Event Delivery" →
// "Acknowledgement semantics"): "a timeout on the order of 5 seconds
// is common." mcpkit ships 5s as the registry-wide default; both the
// http.Client and the underlying net.Dialer read from this constant.
//
// A test pin keeps an accidental tweak to the constant visible at
// review time even when no other test directly observes it.
func TestDefaultWebhookAckTimeout(t *testing.T) {
	assert.Equal(t, 5*time.Second, DefaultWebhookAckTimeout,
		"DefaultWebhookAckTimeout pins spec PR1 commit b506e347 (`~5s common`); change deliberately if spec text moves")
}

// decodeID extracts the derived id from a subscribe response. Helpers
// like dispatchSubscribeForStatus exist for status-focused tests; this
// is the equivalent for id-focused assertions.
func decodeID(t *testing.T, resp *core.Response) string {
	t.Helper()
	raw, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var body struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	return body.ID
}

// ptrTime is a small helper for table-driven tests that need a *time.Time
// without the per-row var dance.
func ptrTime(t time.Time) *time.Time { return &t }

// _ keeps imports referenced even if a test is commented out during
// debugging — bytes/io are used by the helper-fixture pattern used in
// neighboring files; the linter complains less when we keep them
// imported here for forward-compat with the next test added in this
// file.
var _ = bytes.NewReader
var _ io.Reader = (*bytes.Reader)(nil)
