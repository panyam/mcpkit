package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStandardWebhooksSignature_MatchesSpecFormula computes the expected value
// from the specification text rather than from our own signer, which is the
// only way this class of bug is catchable.
//
// The formula is HMAC-SHA256(secret, webhook-id + "." + webhook-timestamp +
// "." + body), base64 with a "v1," prefix, where secret is the base64-decoded
// bytes after the whsec_ prefix. mcpkit keyed the HMAC on the literal string
// for four months. Every verifier it shipped made the same substitution, so
// signer and verifier agreed with each other and disagreed with every other
// implementation; a round-trip test cannot see that.
func TestStandardWebhooksSignature_MatchesSpecFormula(t *testing.T) {
	raw := []byte("0123456789abcdef0123456789abcdef")
	secret := webhookSecretPrefix + base64.RawURLEncoding.EncodeToString(raw)
	body := []byte(`{"eventId":"evt_1"}`)
	msgID := "evt_1"
	now := time.Unix(1700000000, 0)

	mac := hmac.New(sha256.New, raw) // the decoded bytes, per the spec
	mac.Write([]byte(msgID + "." + "1700000000" + "."))
	mac.Write(body)
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	got := signStandardWebhooks(msgID, body, secret, now)
	assert.Equal(t, want, got.headers["webhook-signature"],
		"signature must key the HMAC on the decoded secret, not the whsec_ string")
	assert.True(t,
		VerifyStandardWebhooksSignature(body, secret, msgID,
			got.headers["webhook-timestamp"], want),
		"the verifier must accept a signature computed from the spec formula")
}

// TestSigningKey_DecodesOnlyPrefixedSecrets pins the boundary the round-trip
// test found: a value without the whsec_ prefix is not a spec-format secret,
// and decoding one anyway silently reinterprets any short string that happens
// to be valid base64, which is most of them.
func TestSigningKey_DecodesOnlyPrefixedSecrets(t *testing.T) {
	raw := []byte("0123456789abcdef0123456789abcdef")

	for _, enc := range []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	} {
		assert.Equal(t, raw, signingKey(webhookSecretPrefix+enc),
			"both base64 alphabets must decode, since the SDKs disagree on which they emit")
	}

	assert.Equal(t, []byte("rotated"), signingKey("rotated"),
		"an unprefixed secret is used verbatim")
	assert.Equal(t, []byte(webhookSecretPrefix+"!!not base64!!"),
		signingKey(webhookSecretPrefix+"!!not base64!!"),
		"a prefixed value that cannot decode falls back rather than failing to sign")
}

// TestStandardWebhooksSignature_GeneratedSecretRoundTrips covers the path
// production actually takes: a secret this package minted, signed and verified
// through the exported surface.
func TestStandardWebhooksSignature_GeneratedSecretRoundTrips(t *testing.T) {
	secret := GenerateSecret()
	require.NoError(t, validateClientSecret(secret))

	body := []byte(`{"eventId":"evt_2"}`)
	d := signStandardWebhooks("evt_2", body, secret, time.Now())
	assert.True(t, VerifyStandardWebhooksSignature(
		body, secret, "evt_2",
		d.headers["webhook-timestamp"], d.headers["webhook-signature"],
	))
}
