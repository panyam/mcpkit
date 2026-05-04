package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseHeaderMode_Aliases verifies the flag parser accepts the friendly
// alias forms ("mcp", "standard", "standardwebhooks", "standard-webhooks")
// case-insensitively, and that empty string falls back to the default
// (StandardWebhooks per upstream WG PR#1 line 434, comment r3167245184).
// The CLI flag plumbing depends on this — keeping it loose lets users not
// memorize the exact spelling.
func TestParseHeaderMode_Aliases(t *testing.T) {
	cases := map[string]WebhookHeaderMode{
		"":                  StandardWebhooks,
		"standard":          StandardWebhooks,
		"Standard":          StandardWebhooks,
		"standardwebhooks":  StandardWebhooks,
		"standard-webhooks": StandardWebhooks,
		"mcp":               MCPHeaders,
		"MCP":               MCPHeaders,
	}
	for in, want := range cases {
		got, err := ParseHeaderMode(in)
		require.NoError(t, err, "input=%q", in)
		assert.Equal(t, want, got, "input=%q", in)
	}
}

// TestParseHeaderMode_Unknown verifies unknown tokens are rejected so a
// typo in a CLI flag fails loudly instead of silently picking the default.
func TestParseHeaderMode_Unknown(t *testing.T) {
	_, err := ParseHeaderMode("bogus")
	assert.Error(t, err)
}

// TestSignMCP_ByteFormat pins the exact wire format of the MCP header path:
// X-MCP-Signature is "sha256=<hex>" computed over (ts + "." + body).
// Receivers (including non-Go ones) depend on this exact shape.
//
// signMCP takes a msgID parameter for signature parity with the Standard
// Webhooks signer, but the legacy MCP-headers wire format has no
// webhook-id equivalent — the parameter is ignored. The empty-string
// argument here is intentional.
func TestSignMCP_ByteFormat(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	secret := "topsecret"
	now := time.Unix(1700000000, 0)

	d := signMCP("", body, secret, now)

	assert.Equal(t, "1700000000", d.headers["X-MCP-Timestamp"])
	assert.True(t, strings.HasPrefix(d.headers["X-MCP-Signature"], "sha256="), "sig must start with sha256=")

	// Recompute by hand and compare.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("1700000000"))
	mac.Write([]byte("."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	assert.Equal(t, want, d.headers["X-MCP-Signature"])
	assert.Equal(t, body, d.body, "body passes through unmodified")
}

// TestSignStandardWebhooks_ByteFormat pins the Standard Webhooks v1 wire
// format: signature is "v1,<base64>" computed over (msgId + "." + ts + "." + body),
// and webhook-id is the caller-supplied message identifier (the eventId
// for event deliveries — see TestStandardWebhooks_WebhookIDIsStableAcrossRetries).
// Pinning the byte format protects against accidental drift when refactoring.
func TestSignStandardWebhooks_ByteFormat(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	secret := "topsecret"
	msgID := "evt_demo_42"
	now := time.Unix(1700000000, 0)

	d := signStandardWebhooks(msgID, body, secret, now)

	assert.Equal(t, "1700000000", d.headers["webhook-timestamp"])
	assert.Equal(t, msgID, d.headers["webhook-id"], "webhook-id is the caller-supplied msgID")

	sig := d.headers["webhook-signature"]
	require.True(t, strings.HasPrefix(sig, "v1,"), "sig must start with v1,: got %q", sig)

	// Recompute by hand and compare.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msgID))
	mac.Write([]byte("."))
	mac.Write([]byte("1700000000"))
	mac.Write([]byte("."))
	mac.Write(body)
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	assert.Equal(t, want, sig)
	assert.Equal(t, body, d.body, "body passes through unmodified")
}

// TestStandardWebhooks_WebhookIDIsStableAcrossRetries verifies the spec
// contract: the webhook-id header is stable across retries of the same
// event. Standard Webhooks receivers dedup on webhook-id; if the sender
// regenerates it per retry, the receiver treats each retry as a distinct
// delivery and dedup silently breaks.
//
// This test pins the contract by signing the same event twice with the
// same caller-supplied msgID (simulating two retry attempts) and
// asserting the webhook-id matches across both, while the timestamp +
// signature regenerate (which the spec also requires, so retries aren't
// rejected by the receiver's freshness window).
func TestStandardWebhooks_WebhookIDIsStableAcrossRetries(t *testing.T) {
	body := []byte(`{"eventId":"evt_demo_42","data":{}}`)
	secret := "topsecret"
	eventID := "evt_demo_42"

	first := signStandardWebhooks(eventID, body, secret, time.Unix(1700000000, 0))
	second := signStandardWebhooks(eventID, body, secret, time.Unix(1700000005, 0))

	assert.Equal(t, first.headers["webhook-id"], second.headers["webhook-id"],
		"webhook-id must be stable across retries; receiver dedup depends on it")
	assert.Equal(t, eventID, first.headers["webhook-id"],
		"webhook-id should be the eventId for event deliveries")
	assert.NotEqual(t, first.headers["webhook-timestamp"], second.headers["webhook-timestamp"],
		"timestamp regenerates per retry (spec — so receiver freshness window doesn't reject)")
	assert.NotEqual(t, first.headers["webhook-signature"], second.headers["webhook-signature"],
		"signature regenerates per retry because timestamp is part of the signed input")
}

// TestStandardWebhooks_EmitsSubscriptionIDHeader verifies γ-4's spec
// contract (§"Webhook Event Delivery" L390 + §"Webhook Security" →
// "Signature scheme" L472): every delivery MUST include
// X-MCP-Subscription-Id carrying the spec's derived subscription id
// so the receiver can select the correct secret without parsing the
// body.
//
// Without this header, a receiver hosting multiple subscriptions on a
// single endpoint would have no way to pick the correct verification
// secret — the body has eventId + name but not the subscription id.
func TestStandardWebhooks_EmitsSubscriptionIDHeader(t *testing.T) {
	body := []byte(`{"eventId":"evt_1","data":{}}`)
	signed := signStandardWebhooks("evt_1", body, "whsec_test", time.Unix(1700000000, 0)).
		withSubscriptionID("sub_abc123")

	got, ok := signed.headers["X-MCP-Subscription-Id"]
	require.True(t, ok, "X-MCP-Subscription-Id header MUST be present on every delivery")
	assert.Equal(t, "sub_abc123", got)
}

// TestStandardWebhooks_SubscriptionIDStableAcrossRetries pairs with
// TestStandardWebhooks_WebhookIDIsStableAcrossRetries (which verifies
// webhook-id stability for receiver dedup). The X-MCP-Subscription-Id
// must ALSO be stable across retries — same subscription, same routing
// handle. If it changed per retry, a receiver tracking deliveries by
// subscription would log spurious churn or worse, mis-route to a
// non-existent subscription.
func TestStandardWebhooks_SubscriptionIDStableAcrossRetries(t *testing.T) {
	body := []byte(`{"eventId":"evt_42","data":{}}`)
	subID := "sub_stable_test"

	first := signStandardWebhooks("evt_42", body, "whsec_test", time.Unix(1700000000, 0)).
		withSubscriptionID(subID)
	second := signStandardWebhooks("evt_42", body, "whsec_test", time.Unix(1700000005, 0)).
		withSubscriptionID(subID)

	assert.Equal(t, first.headers["X-MCP-Subscription-Id"], second.headers["X-MCP-Subscription-Id"],
		"X-MCP-Subscription-Id must be stable across retries of the same event")
	assert.Equal(t, subID, first.headers["X-MCP-Subscription-Id"])
}

// TestStandardWebhooks_WithEmptySubscriptionIDIsNoOp verifies that
// withSubscriptionID("") leaves the headers untouched. Defensive — the
// registry's deliver path always passes target.ID (always set post-γ-2),
// but the empty-string case shouldn't crash or add a misleading
// "X-MCP-Subscription-Id: " header.
func TestStandardWebhooks_WithEmptySubscriptionIDIsNoOp(t *testing.T) {
	body := []byte(`{}`)
	signed := signStandardWebhooks("evt_1", body, "whsec_test", time.Unix(1700000000, 0)).
		withSubscriptionID("")

	_, present := signed.headers["X-MCP-Subscription-Id"]
	assert.False(t, present, "empty subscription id must not add the header at all")
}

// TestNewMessageID_Uniqueness verifies the helper produces distinct ids
// across calls. Without this, replay-detection logic on receivers that
// dedupe by webhook-id would silently break.
func TestNewMessageID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := newMessageID()
		_, dup := seen[id]
		require.False(t, dup, "duplicate message id at iteration %d: %s", i, id)
		seen[id] = struct{}{}
	}
}

// TestVerifyMCPSignature_RoundTrip verifies the verifier accepts a freshly
// signed payload (positive case) and rejects a tampered body, swapped
// timestamp, or wrong secret (negative cases).
func TestVerifyMCPSignature_RoundTrip(t *testing.T) {
	body := []byte(`{"x":1}`)
	secret := "k"
	now := time.Unix(1700000000, 0)

	d := signMCP("", body, secret, now)
	ts := d.headers["X-MCP-Timestamp"]
	sig := d.headers["X-MCP-Signature"]

	assert.True(t, VerifyMCPSignature(body, secret, ts, sig), "valid signature should verify")
	assert.False(t, VerifyMCPSignature([]byte(`{"x":2}`), secret, ts, sig), "tampered body must fail")
	assert.False(t, VerifyMCPSignature(body, "wrong", ts, sig), "wrong secret must fail")
	assert.False(t, VerifyMCPSignature(body, secret, "1700000001", sig), "wrong timestamp must fail")
}

// TestVerifyStandardWebhooksSignature_RoundTrip verifies the Standard
// Webhooks verifier accepts freshly signed deliveries and rejects mutations.
// Also verifies the multi-signature tolerance — the spec allows space-
// separated versioned signatures, and we must accept any matching v1.
func TestVerifyStandardWebhooksSignature_RoundTrip(t *testing.T) {
	body := []byte(`{"x":1}`)
	secret := "k"
	now := time.Unix(1700000000, 0)

	msgID := "evt_test_round_trip"
	d := signStandardWebhooks(msgID, body, secret, now)
	ts := d.headers["webhook-timestamp"]
	sig := d.headers["webhook-signature"]

	assert.True(t, VerifyStandardWebhooksSignature(body, secret, msgID, ts, sig))
	assert.False(t, VerifyStandardWebhooksSignature([]byte(`{"x":2}`), secret, msgID, ts, sig), "tampered body must fail")
	assert.False(t, VerifyStandardWebhooksSignature(body, "wrong", msgID, ts, sig), "wrong secret must fail")
	assert.False(t, VerifyStandardWebhooksSignature(body, secret, "msg_other", ts, sig), "wrong message id must fail")

	// Multi-signature header (spec-allowed): rotation case where a sender
	// emits old + new signatures simultaneously.
	mac := hmac.New(sha256.New, []byte("rotated"))
	mac.Write([]byte(msgID))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	rotated := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	combined := sig + " " + rotated
	assert.True(t, VerifyStandardWebhooksSignature(body, secret, msgID, ts, combined), "any matching v1 should verify")
	assert.True(t, VerifyStandardWebhooksSignature(body, "rotated", msgID, ts, combined), "rotated key matches the second v1")
}

// TestSignFor_DispatchesByMode verifies the dispatcher picks the right
// signer per mode — guarantees the registry's mode field actually controls
// which header set goes on the wire.
func TestSignFor_DispatchesByMode(t *testing.T) {
	body := []byte(`{}`)
	now := time.Unix(1700000000, 0)

	mcp := signFor(MCPHeaders, "evt_1", body, "k", now)
	assert.Contains(t, mcp.headers, "X-MCP-Signature")
	assert.NotContains(t, mcp.headers, "webhook-signature")

	std := signFor(StandardWebhooks, "evt_1", body, "k", now)
	assert.Contains(t, std.headers, "webhook-signature")
	assert.NotContains(t, std.headers, "X-MCP-Signature")
	assert.Equal(t, "evt_1", std.headers["webhook-id"], "webhook-id is the supplied msgID")
}
