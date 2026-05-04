package events

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCanonicalKey_DeterministicForSameInputs pins the foundational
// idempotency property: two subscribe calls with identical
// (principal, url, name, params) MUST produce identical canonical bytes
// — that's how the registry knows "this is the same subscription, refresh
// it" instead of creating a duplicate. (§"Subscription Identity" →
// "Key composition" L363).
func TestCanonicalKey_DeterministicForSameInputs(t *testing.T) {
	a := canonicalKey("alice", "https://example.com/hook", "incident.created",
		map[string]any{"severity": "P1", "service": "api"})
	b := canonicalKey("alice", "https://example.com/hook", "incident.created",
		map[string]any{"severity": "P1", "service": "api"})
	assert.Equal(t, a, b, "same inputs MUST produce identical canonical bytes — idempotency depends on it")
}

// TestCanonicalKey_ParamMapOrderInvariant verifies the canonical-JSON
// requirement (L363 "params is compared by canonical-JSON equality"):
// param map iteration order must NOT affect the bytes. Without this,
// the same subscribe call could non-deterministically derive different
// ids across runs.
func TestCanonicalKey_ParamMapOrderInvariant(t *testing.T) {
	// Two maps with same content but Go's map randomization could
	// iterate them differently on each call. The canonical encoder
	// must sort the keys.
	a := canonicalKey("alice", "u", "n", map[string]any{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
	})
	b := canonicalKey("alice", "u", "n", map[string]any{
		"e": "5", "d": "4", "c": "3", "b": "2", "a": "1",
	})
	assert.Equal(t, a, b, "param map order must not affect canonical bytes")
}

// TestCanonicalKey_DifferentPrincipalDifferentKey is the spec's
// cross-tenant isolation property in test form (§"Subscription Identity" →
// "Cross-tenant isolation" L378): two distinct tenants subscribing to the
// same (name, params, url) MUST get distinct subscriptions. Without
// principal in the key the tuple would be guessable across tenants.
func TestCanonicalKey_DifferentPrincipalDifferentKey(t *testing.T) {
	alice := canonicalKey("alice", "u", "n", map[string]any{"x": "1"})
	bob := canonicalKey("bob", "u", "n", map[string]any{"x": "1"})
	assert.NotEqual(t, alice, bob, "different principals must canonicalize to different keys (cross-tenant isolation)")
}

// TestCanonicalKey_DifferentURLDifferentKey verifies subs to different
// callback URLs (e.g., one tenant's proxy vs another's) get distinct
// canonical bytes even with same (principal, name, params).
func TestCanonicalKey_DifferentURLDifferentKey(t *testing.T) {
	a := canonicalKey("alice", "https://a.example.com/hook", "n", nil)
	b := canonicalKey("alice", "https://b.example.com/hook", "n", nil)
	assert.NotEqual(t, a, b)
}

// TestCanonicalKey_DifferentNameDifferentKey — pretty obvious but
// pinning prevents accidental loss in any future refactor.
func TestCanonicalKey_DifferentNameDifferentKey(t *testing.T) {
	a := canonicalKey("alice", "u", "incident.created", nil)
	b := canonicalKey("alice", "u", "incident.resolved", nil)
	assert.NotEqual(t, a, b)
}

// TestCanonicalKey_DifferentParamsDifferentKey verifies any change in
// params changes the canonical bytes — even one field flip.
func TestCanonicalKey_DifferentParamsDifferentKey(t *testing.T) {
	a := canonicalKey("alice", "u", "n", map[string]any{"region": "us"})
	b := canonicalKey("alice", "u", "n", map[string]any{"region": "eu"})
	assert.NotEqual(t, a, b)
}

// TestCanonicalKey_NilAndEmptyParamsEquivalent verifies the corner case
// that callers passing nil and callers passing an empty map get the
// same canonical bytes. Otherwise some clients would derive different
// ids based on whether they sent `params: {}` vs omitted the field.
func TestCanonicalKey_NilAndEmptyParamsEquivalent(t *testing.T) {
	withNil := canonicalKey("alice", "u", "n", nil)
	withEmpty := canonicalKey("alice", "u", "n", map[string]any{})
	assert.Equal(t, withNil, withEmpty, "nil and empty params must canonicalize identically")
}

// TestCanonicalKey_FieldsAreSeparated checks that a crafted principal
// containing a value that LOOKS like a URL doesn't merge into the URL
// field via concatenation. This is why we use 0x1f (Unit Separator) as
// the joiner — it's non-printable and won't appear in any reasonable
// principal/url/name/params content.
func TestCanonicalKey_FieldsAreSeparated(t *testing.T) {
	// "alice" + sep + "u" + sep + "n" + sep + "{}"  vs
	// "alice" + sep + "u" + sep + "n" + sep + "{}"  with a crafted
	// principal that tries to inject a separator-like character.
	// If the joiner were ":" then principal="alice:https://evil.com"
	// could collide with principal="alice", url="https://evil.com".
	// Using 0x1f makes that impossible because 0x1f doesn't render
	// or get accepted in OAuth subject claims.
	normal := canonicalKey("alice", "https://victim.example/hook", "n", nil)
	crafted := canonicalKey("alice:https://victim.example/hook", "alt-url", "n", nil)
	assert.NotEqual(t, normal, crafted, "field separator must prevent injection of canonical-key fields via principal content")
}

// TestDeriveSubscriptionID_DeterministicAndFormatted pins the routing-id
// derivation: stable for fixed canonical bytes, "sub_" prefix for
// recognizability in logs / headers.
func TestDeriveSubscriptionID_DeterministicAndFormatted(t *testing.T) {
	canonical := canonicalKey("alice", "u", "n", map[string]any{"x": "1"})
	id1 := deriveSubscriptionID(canonical)
	id2 := deriveSubscriptionID(canonical)
	assert.Equal(t, id1, id2, "derivation must be stable for the same canonical bytes")
	assert.True(t, strings.HasPrefix(id1, "sub_"), "id must start with sub_ prefix; got %q", id1)
}

// TestDeriveSubscriptionID_DifferentInputsDifferentID checks that the
// hash function actually differentiates different canonical-byte inputs.
// Sanity check that we're not using a constant.
func TestDeriveSubscriptionID_DifferentInputsDifferentID(t *testing.T) {
	a := deriveSubscriptionID(canonicalKey("alice", "u", "n", nil))
	b := deriveSubscriptionID(canonicalKey("bob", "u", "n", nil))
	assert.NotEqual(t, a, b)
}

// TestDeriveSubscriptionID_ReasonableLength checks the routing handle
// stays in a reasonable size range for a header value. 16 bytes
// base64-encoded with raw-url encoding is 22 chars; with the "sub_"
// prefix the total is 26.
func TestDeriveSubscriptionID_ReasonableLength(t *testing.T) {
	id := deriveSubscriptionID(canonicalKey("alice", "u", "n", nil))
	assert.Equal(t, 26, len(id), "expected sub_ + 22-char base64 of 16 bytes; got %d-char id %q", len(id), id)
}

// TestCanonicalKey_NoCollisionForKnownInputs is a smoke test against
// accidental collisions among a small set of plausible inputs. With
// 128-bit truncation the birthday bound is ~2^64 — at this small scale
// no collisions are expected, and any collision here would be a coding
// regression in canonicalKey or deriveSubscriptionID.
func TestCanonicalKey_NoCollisionForKnownInputs(t *testing.T) {
	inputs := []struct {
		principal, url, name string
		params               map[string]any
	}{
		{"alice", "https://a.com/hook", "incident.created", map[string]any{"severity": "P1"}},
		{"alice", "https://a.com/hook", "incident.created", map[string]any{"severity": "P2"}},
		{"alice", "https://a.com/hook", "incident.resolved", map[string]any{"severity": "P1"}},
		{"alice", "https://b.com/hook", "incident.created", map[string]any{"severity": "P1"}},
		{"bob", "https://a.com/hook", "incident.created", map[string]any{"severity": "P1"}},
		{"alice", "https://a.com/hook", "incident.created", nil},
	}
	seen := make(map[string]int, len(inputs))
	for i, in := range inputs {
		id := deriveSubscriptionID(canonicalKey(in.principal, in.url, in.name, in.params))
		if prev, dup := seen[id]; dup {
			t.Errorf("collision: input %d (%+v) and input %d (%+v) both derive %s",
				prev, inputs[prev], i, in, id)
		}
		seen[id] = i
	}
}
