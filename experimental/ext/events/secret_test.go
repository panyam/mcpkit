package events

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseSecretMode_Aliases verifies the flag parser accepts the friendly
// alias forms case-insensitively, and that empty string falls back to the
// default (WebhookSecretServer).
func TestParseSecretMode_Aliases(t *testing.T) {
	cases := map[string]WebhookSecretMode{
		"":         WebhookSecretServer,
		"server":   WebhookSecretServer,
		"SERVER":   WebhookSecretServer,
		"client":   WebhookSecretClient,
		"Client":   WebhookSecretClient,
		"identity": WebhookSecretIdentity,
		"Identity": WebhookSecretIdentity,
	}
	for in, want := range cases {
		got, err := ParseSecretMode(in)
		require.NoError(t, err, "input=%q", in)
		assert.Equal(t, want, got, "input=%q", in)
	}
}

// TestParseSecretMode_Unknown verifies typos error rather than silently
// defaulting — protects users from picking an unintended mode.
func TestParseSecretMode_Unknown(t *testing.T) {
	_, err := ParseSecretMode("identityy")
	assert.Error(t, err)
}

// TestGenerateSecret_PrefixAndEntropy verifies generated secrets have the
// expected wire-recognizable prefix and meaningful entropy (distinct on
// repeated calls).
func TestGenerateSecret_PrefixAndEntropy(t *testing.T) {
	a := generateSecret()
	b := generateSecret()
	assert.True(t, strings.HasPrefix(a, "whsec_"), "must start with whsec_")
	assert.True(t, strings.HasPrefix(b, "whsec_"))
	assert.NotEqual(t, a, b, "two generated secrets must differ")
	assert.Greater(t, len(a), 30, "secret should carry meaningful entropy")
}

// TestCanonicalTuple_Stable verifies the canonical-tuple helper produces
// identical bytes for identical inputs and diverges on any field change.
// This is the foundation of identity-mode determinism: if canonicalization
// drifts, the same subscription request would derive different secrets
// across calls and break idempotency.
func TestCanonicalTuple_Stable(t *testing.T) {
	a := canonicalTuple("alert.fired", "https://example.com/hook", map[string]string{"region": "us"})
	b := canonicalTuple("alert.fired", "https://example.com/hook", map[string]string{"region": "us"})
	assert.Equal(t, a, b, "same inputs must produce same canonical bytes")

	c := canonicalTuple("alert.fired", "https://example.com/hook", map[string]string{"region": "eu"})
	assert.NotEqual(t, a, c, "different params must produce different canonical bytes")

	d := canonicalTuple("alert.fired", "https://other.example.com/hook", map[string]string{"region": "us"})
	assert.NotEqual(t, a, d, "different url must produce different canonical bytes")

	e := canonicalTuple("incident.fired", "https://example.com/hook", map[string]string{"region": "us"})
	assert.NotEqual(t, a, e, "different name must produce different canonical bytes")
}

// TestCanonicalTuple_ParamOrderInvariant verifies that param map iteration
// order does not affect the canonical bytes — keys are sorted internally.
// Without this, identity mode would non-deterministically derive different
// secrets for the same logical subscription.
func TestCanonicalTuple_ParamOrderInvariant(t *testing.T) {
	a := canonicalTuple("x", "u", map[string]string{"a": "1", "b": "2", "c": "3"})
	b := canonicalTuple("x", "u", map[string]string{"c": "3", "a": "1", "b": "2"})
	assert.Equal(t, a, b)
}

// TestCanonicalTuple_EmptyParams verifies nil and empty maps are treated
// equivalently and produce the same canonical bytes.
func TestCanonicalTuple_EmptyParams(t *testing.T) {
	a := canonicalTuple("x", "u", nil)
	b := canonicalTuple("x", "u", map[string]string{})
	assert.Equal(t, a, b)
}

// TestDeriveIdentitySecret_Deterministic verifies the secret derivation is
// stable for fixed (root, tuple) — which is the whole point of identity
// mode: subscribing twice with the same tuple yields the same secret.
func TestDeriveIdentitySecret_Deterministic(t *testing.T) {
	root := []byte("master-root")
	a := deriveIdentitySecret(root, "x", "u", map[string]string{"k": "v"})
	b := deriveIdentitySecret(root, "x", "u", map[string]string{"k": "v"})
	assert.Equal(t, a, b)
	assert.True(t, strings.HasPrefix(a, "whsec_"))
}

// TestDeriveIdentitySecret_DiffersByRoot verifies rotating the root produces
// a different secret for the same tuple. Operationally this is the rotation
// path: change the root → all derived secrets change → effectively rotates
// every subscription at once.
func TestDeriveIdentitySecret_DiffersByRoot(t *testing.T) {
	a := deriveIdentitySecret([]byte("root-a"), "x", "u", nil)
	b := deriveIdentitySecret([]byte("root-b"), "x", "u", nil)
	assert.NotEqual(t, a, b)
}

// TestDeriveIdentityID_Deterministic verifies the derived id is stable for
// fixed inputs. Receivers route by id, so non-determinism would cause
// route lookups to fail across subscribe calls.
func TestDeriveIdentityID_Deterministic(t *testing.T) {
	a := deriveIdentityID("x", "u", map[string]string{"k": "v"})
	b := deriveIdentityID("x", "u", map[string]string{"k": "v"})
	assert.Equal(t, a, b)
	assert.True(t, strings.HasPrefix(a, "sub_"))
}
