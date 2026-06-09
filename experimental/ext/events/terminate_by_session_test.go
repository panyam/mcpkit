package events

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for TerminateBySession + TerminateBySubject — the
// WebhookRegistry-side of the OIDC Back-Channel Logout wire-up.
// See ext/auth/back_channel_logout.go + issue 709 for the AS-facing
// half. These tests exercise the lib in isolation; the integration
// test (BCL POST → handler → listener → TerminateBySession) lives in
// the whole-enchilada demo wiring once Keycloak's BCL trigger is
// reproducible end-to-end.

// TestTerminateBySession_KillsMatchingSubscriptionsAndPosts verifies
// that a TerminateBySession call walks every subscription in the
// store, removes the ones whose SessionID matches, and POSTs a
// {type:"terminated"} envelope to each. Non-matching subscriptions
// must remain untouched.
func TestTerminateBySession_KillsMatchingSubscriptionsAndPosts(t *testing.T) {
	receiverA1 := &captureControlPost{}
	receiverA2 := &captureControlPost{}
	receiverB := &captureControlPost{}
	srvA1 := httptest.NewServer(receiverA1.handler())
	srvA2 := httptest.NewServer(receiverA2.handler())
	srvB := httptest.NewServer(receiverB.handler())
	defer srvA1.Close()
	defer srvA2.Close()
	defer srvB.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))

	// Two subscriptions tied to session-alice, one to session-bob.
	register := func(principal, sub, sid, url string) {
		canonical := canonicalKey(principal, url, "fake.event", nil)
		r.Register(RegisterParams{
			CanonicalKey: canonical,
			DerivedID:    deriveSubscriptionID(canonical),
			URL:          url,
			Secret:       "whsec_" + strings.Repeat("a", 32),
			Subject:      sub,
			SessionID:    sid,
		})
	}
	register("tenant-a/alice", "alice", "session-alice", srvA1.URL)
	register("tenant-a/alice", "alice", "session-alice", srvA2.URL)
	register("tenant-a/bob", "bob", "session-bob", srvB.URL)

	killed := r.TerminateBySession("session-alice", ControlError{
		Code:    -32012,
		Message: "session revoked by AS",
	})
	assert.Equal(t, 2, killed, "exactly the two alice subscriptions terminate")

	// Both alice receivers got a {type:terminated} envelope; bob's didn't.
	require.Eventually(t, func() bool {
		receiverA1.mu.Lock()
		defer receiverA1.mu.Unlock()
		receiverA2.mu.Lock()
		defer receiverA2.mu.Unlock()
		return receiverA1.hits == 1 && receiverA2.hits == 1
	}, 2*time.Second, 20*time.Millisecond, "both alice receivers should get one terminated POST")

	receiverB.mu.Lock()
	assert.Equal(t, 0, receiverB.hits, "bob's receiver must NOT get any POST")
	receiverB.mu.Unlock()

	// Body shape — {type:"terminated", error:{code, message}}.
	for _, recv := range []*captureControlPost{receiverA1, receiverA2} {
		recv.mu.Lock()
		var env struct {
			Type  string `json:"type"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(recv.body, &env))
		assert.Equal(t, "terminated", env.Type)
		assert.Equal(t, -32012, env.Error.Code)
		assert.Contains(t, env.Error.Message, "session revoked")
		recv.mu.Unlock()
	}
}

// TestTerminateBySession_EmptySidIsNoop verifies the guard: an empty
// sid argument is a no-op (returns 0), never accidentally matches the
// targets whose stored SessionID is also empty (anonymous /
// non-OIDC subscriptions).
func TestTerminateBySession_EmptySidIsNoop(t *testing.T) {
	recv := &captureControlPost{}
	srv := httptest.NewServer(recv.handler())
	defer srv.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	canonical := canonicalKey("anon", srv.URL, "fake.event", nil)
	r.Register(RegisterParams{
		CanonicalKey: canonical,
		DerivedID:    deriveSubscriptionID(canonical),
		URL:          srv.URL,
		Secret:       "whsec_" + strings.Repeat("a", 32),
		// SessionID intentionally empty — anonymous subscription.
	})

	killed := r.TerminateBySession("", ControlError{Code: -32012, Message: "x"})
	assert.Equal(t, 0, killed, "empty sid must not terminate anything")
	time.Sleep(100 * time.Millisecond) // give any rogue POST a chance to land
	recv.mu.Lock()
	assert.Equal(t, 0, recv.hits)
	recv.mu.Unlock()
}

// TestTerminateBySubject_KillsAllSessionsForSubject covers the
// fallback path when the AS's logout_token carries only `sub`. Every
// subscription whose stored Subject matches must terminate, regardless
// of session id.
func TestTerminateBySubject_KillsAllSessionsForSubject(t *testing.T) {
	recv1 := &captureControlPost{}
	recv2 := &captureControlPost{}
	other := &captureControlPost{}
	srv1 := httptest.NewServer(recv1.handler())
	srv2 := httptest.NewServer(recv2.handler())
	srvOther := httptest.NewServer(other.handler())
	defer srv1.Close()
	defer srv2.Close()
	defer srvOther.Close()

	r := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	register := func(sub, sid, url string) {
		canonical := canonicalKey("tenant-a/"+sub, url, "fake.event", nil)
		r.Register(RegisterParams{
			CanonicalKey: canonical,
			DerivedID:    deriveSubscriptionID(canonical),
			URL:          url,
			Secret:       "whsec_" + strings.Repeat("a", 32),
			Subject:      sub,
			SessionID:    sid,
		})
	}
	// alice has two distinct sessions (e.g., two devices).
	register("alice", "alice-sess-1", srv1.URL)
	register("alice", "alice-sess-2", srv2.URL)
	register("bob", "bob-sess-1", srvOther.URL)

	killed := r.TerminateBySubject("alice", ControlError{Code: -32012, Message: "subject revoked"})
	assert.Equal(t, 2, killed)

	require.Eventually(t, func() bool {
		recv1.mu.Lock()
		defer recv1.mu.Unlock()
		recv2.mu.Lock()
		defer recv2.mu.Unlock()
		return recv1.hits == 1 && recv2.hits == 1
	}, 2*time.Second, 20*time.Millisecond)

	other.mu.Lock()
	assert.Equal(t, 0, other.hits, "bob's subscription stays alive")
	other.mu.Unlock()
}
