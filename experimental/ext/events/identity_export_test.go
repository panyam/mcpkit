package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCanonicalKey_PrincipalIsPartOfIdentity is the property the spec's key
// composition rule exists for, and the one a cross-tenant conformance check
// probes: two tenants subscribing to the same event with the same callback get
// distinct subscriptions, because the principal is in the key.
func TestCanonicalKey_PrincipalIsPartOfIdentity(t *testing.T) {
	args := map[string]any{"channel": "general"}
	a := CanonicalKey("tenant-a", "https://example.test/hook", "chat.message", args)
	b := CanonicalKey("tenant-b", "https://example.test/hook", "chat.message", args)

	assert.NotEqual(t, a, b, "the principal must change the canonical key")
	assert.NotEqual(t, DeriveSubscriptionID(a), DeriveSubscriptionID(b),
		"distinct keys must derive distinct subscription ids")
}

// TestCanonicalKey_StableAcrossArgumentOrder covers the half a caller is most
// likely to get wrong by hand: arguments compare by canonical JSON, so map key
// order cannot matter and nil must equal empty.
func TestCanonicalKey_StableAcrossArgumentOrder(t *testing.T) {
	one := CanonicalKey("t", "https://example.test/h", "e",
		map[string]any{"a": 1, "b": 2})
	two := CanonicalKey("t", "https://example.test/h", "e",
		map[string]any{"b": 2, "a": 1})
	assert.Equal(t, one, two, "argument key order must not change the key")

	assert.Equal(t,
		CanonicalKey("t", "https://example.test/h", "e", nil),
		CanonicalKey("t", "https://example.test/h", "e", map[string]any{}),
		"nil and empty arguments must produce the same key")
}

// TestDeriveSubscriptionID_MatchesTheHandler pins the exported pair to what the
// subscribe handler actually registers. If these drift, a fixture or store
// computing identity from the public API silently addresses a subscription the
// registry does not have.
func TestDeriveSubscriptionID_MatchesTheHandler(t *testing.T) {
	args := map[string]any{"severity": "high"}
	exported := DeriveSubscriptionID(CanonicalKey("p", "https://h.test/x", "alert.fired", args))
	internal := deriveSubscriptionID(canonicalKey("p", "https://h.test/x", "alert.fired", args))
	assert.Equal(t, internal, exported)
	assert.Contains(t, exported, "sub_")
}
