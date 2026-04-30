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
// case-insensitively, and that empty string falls back to the default. The
// CLI flag plumbing depends on this — keeping it loose lets users not memorize
// the exact spelling.
func TestParseHeaderMode_Aliases(t *testing.T) {
	cases := map[string]WebhookHeaderMode{
		"":                  MCPHeaders,
		"mcp":               MCPHeaders,
		"MCP":               MCPHeaders,
		"standard":          StandardWebhooks,
		"Standard":          StandardWebhooks,
		"standardwebhooks":  StandardWebhooks,
		"standard-webhooks": StandardWebhooks,
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
func TestSignMCP_ByteFormat(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	secret := "topsecret"
	now := time.Unix(1700000000, 0)

	d := signMCP(body, secret, now)

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
// format: signature is "v1,<base64>" computed over (msgId + "." + ts + "." + body)
// and a webhook-id is generated per delivery. Pinning the byte format protects
// against accidental drift when refactoring.
func TestSignStandardWebhooks_ByteFormat(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	secret := "topsecret"
	now := time.Unix(1700000000, 0)

	d := signStandardWebhooks(body, secret, now)

	assert.Equal(t, "1700000000", d.headers["webhook-timestamp"])
	require.NotEmpty(t, d.headers["webhook-id"], "webhook-id must be populated")
	require.True(t, strings.HasPrefix(d.headers["webhook-id"], "msg_"))

	sig := d.headers["webhook-signature"]
	require.True(t, strings.HasPrefix(sig, "v1,"), "sig must start with v1,: got %q", sig)

	// Recompute using the generated webhook-id and compare.
	msgID := d.headers["webhook-id"]
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

	d := signMCP(body, secret, now)
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

	d := signStandardWebhooks(body, secret, now)
	msgID := d.headers["webhook-id"]
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

	mcp := signFor(MCPHeaders, body, "k", now)
	assert.Contains(t, mcp.headers, "X-MCP-Signature")
	assert.NotContains(t, mcp.headers, "webhook-signature")

	std := signFor(StandardWebhooks, body, "k", now)
	assert.Contains(t, std.headers, "webhook-signature")
	assert.NotContains(t, std.headers, "X-MCP-Signature")
}
