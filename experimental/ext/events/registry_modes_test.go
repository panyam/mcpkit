package events

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewWebhookRegistry_IdentityRequiresRoot verifies the registry refuses
// to construct in Identity mode without a root — the config bug is caught
// at process start instead of at first subscribe (where a derivation against
// a nil root would silently produce a degenerate secret).
func TestNewWebhookRegistry_IdentityRequiresRoot(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "constructing identity mode without root must panic")
		assert.Contains(t, r.(string), "WithWebhookRoot")
	}()
	_ = NewWebhookRegistry(WithWebhookSecretMode(WebhookSecretIdentity))
}

// TestNewWebhookRegistry_DefaultsToServerMode pins the default mode so that a
// future change to the default is a deliberate, visible edit (and triggers
// downstream test updates) rather than a silent shift.
func TestNewWebhookRegistry_DefaultsToServerMode(t *testing.T) {
	r := NewWebhookRegistry()
	assert.Equal(t, WebhookSecretServer, r.SecretMode())
}

// TestResolveSecret_ServerModeIgnoresClient verifies that in Server mode the
// supplied secret is dropped on the floor and a fresh one is generated.
// This is the entropy-QA argument from Casey on PR#1 line 391: the server
// shouldn't trust client entropy.
func TestResolveSecret_ServerModeIgnoresClient(t *testing.T) {
	r := NewWebhookRegistry() // Server is default
	_, secret := r.resolveSecret("client-id", "client-supplied", "alert.fired", "https://x", nil)
	assert.NotEqual(t, "client-supplied", secret, "server mode must not honour client secret")
	assert.True(t, strings.HasPrefix(secret, "whsec_"), "server-generated secret must use the whsec_ prefix")
}

// TestResolveSecret_ClientModeHonoursSupplied verifies that in Client mode
// the client-supplied secret round-trips unchanged. This is the IaC use
// case (Aman Singh on line 391): the gateway has the secret pre-provisioned
// before subscribe is called.
func TestResolveSecret_ClientModeHonoursSupplied(t *testing.T) {
	r := NewWebhookRegistry(WithWebhookSecretMode(WebhookSecretClient))
	id, secret := r.resolveSecret("client-id", "client-supplied", "alert.fired", "https://x", nil)
	assert.Equal(t, "client-id", id)
	assert.Equal(t, "client-supplied", secret)
}

// TestResolveSecret_ClientModeFallsBackWhenEmpty verifies that Client mode
// generates a secret when the client supplies none — preserves usability
// for clients that don't bring their own.
func TestResolveSecret_ClientModeFallsBackWhenEmpty(t *testing.T) {
	r := NewWebhookRegistry(WithWebhookSecretMode(WebhookSecretClient))
	_, secret := r.resolveSecret("client-id", "", "alert.fired", "https://x", nil)
	assert.True(t, strings.HasPrefix(secret, "whsec_"))
}

// TestResolveSecret_IdentityModeIsDeterministic verifies Identity mode's
// core property: same tuple → same id and secret. This is what makes the
// subscribe operation idempotent at the HTTP layer.
func TestResolveSecret_IdentityModeIsDeterministic(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookSecretMode(WebhookSecretIdentity),
		WithWebhookRoot([]byte("root")),
	)
	id1, sec1 := r.resolveSecret("ignored", "ignored", "alert.fired", "https://x", map[string]string{"region": "us"})
	id2, sec2 := r.resolveSecret("also-ignored", "also-ignored", "alert.fired", "https://x", map[string]string{"region": "us"})
	assert.Equal(t, id1, id2, "identity mode must derive the same id for the same tuple")
	assert.Equal(t, sec1, sec2, "identity mode must derive the same secret for the same tuple")
}

// TestResolveSecret_IdentityModeIgnoresClientInputs verifies that the
// client-supplied id and secret are NOT what the registry stores in
// Identity mode. Together with the determinism test above, this proves
// the only inputs that affect identity-mode output are (name, url, params).
func TestResolveSecret_IdentityModeIgnoresClientInputs(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookSecretMode(WebhookSecretIdentity),
		WithWebhookRoot([]byte("root")),
	)
	id, secret := r.resolveSecret("client-id", "client-secret", "alert.fired", "https://x", nil)
	assert.NotEqual(t, "client-id", id)
	assert.NotEqual(t, "client-secret", secret)
	assert.True(t, strings.HasPrefix(id, "sub_"))
	assert.True(t, strings.HasPrefix(secret, "whsec_"))
}

// TestRegister_IdentityModeIsIdempotent verifies that two registrations
// against the same tuple produce a single registry entry, not two. This is
// the user-visible consequence of identity-mode determinism: a re-subscribe
// is effectively a refresh, not a new subscription.
func TestRegister_IdentityModeIsIdempotent(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookSecretMode(WebhookSecretIdentity),
		WithWebhookRoot([]byte("root")),
	)
	id1, sec1 := r.resolveSecret("", "", "alert.fired", "https://x", nil)
	id2, sec2 := r.resolveSecret("", "", "alert.fired", "https://x", nil)
	require.Equal(t, id1, id2)
	require.Equal(t, sec1, sec2)

	r.Register(id1, "https://x", sec1)
	r.Register(id2, "https://x", sec2)

	assert.Len(t, r.Targets(), 1, "two identity-mode subscribes against the same tuple must collapse to one entry")
}

// TestUnregisterBySecret_RemovesMatching verifies the proof-of-possession
// unsubscribe path removes only the subscription matching the supplied
// (url, secret) — leaves other URLs and other secrets at the same URL alone.
func TestUnregisterBySecret_RemovesMatching(t *testing.T) {
	r := NewWebhookRegistry()
	r.Register("a", "https://x", "secret-a")
	r.Register("b", "https://x", "secret-b")
	r.Register("c", "https://y", "secret-a") // same secret string, different URL

	r.UnregisterBySecret("https://x", "secret-a")

	urls := map[string]string{}
	for _, t := range r.Targets() {
		urls[t.ID] = t.URL
	}
	assert.NotContains(t, urls, "a", "matching (url, secret) entry must be removed")
	assert.Contains(t, urls, "b", "different secret at same URL must remain")
	assert.Contains(t, urls, "c", "same secret at different URL must remain")
}

// TestUnregisterBySecret_NoMatchIsNoOp verifies passing a secret no
// subscription has is a silent no-op rather than an error. Mirrors
// Unregister(id) behaviour and avoids leaking via error vs no-op timing.
func TestUnregisterBySecret_NoMatchIsNoOp(t *testing.T) {
	r := NewWebhookRegistry()
	r.Register("a", "https://x", "secret-a")
	r.UnregisterBySecret("https://x", "wrong-secret")
	assert.Len(t, r.Targets(), 1)
}

// TestUnregisterBySecret_EmptySecretIsNoOp verifies that an empty secret
// can never match — protects against a callsite that didn't supply a
// secret accidentally wiping everything that has empty Secret in storage.
func TestUnregisterBySecret_EmptySecretIsNoOp(t *testing.T) {
	r := NewWebhookRegistry()
	r.Register("a", "https://x", "")
	r.UnregisterBySecret("https://x", "")
	assert.Len(t, r.Targets(), 1, "empty secret must never match")
}

// TestRegister_TTLAppliesPerMode verifies the TTL configuration interacts
// cleanly with the identity-mode dedupe path — a re-subscribe refreshes
// the existing entry's expiry rather than creating a parallel one.
func TestRegister_TTLAppliesPerMode(t *testing.T) {
	r := NewWebhookRegistry(
		WithWebhookSecretMode(WebhookSecretIdentity),
		WithWebhookRoot([]byte("root")),
		WithWebhookTTL(50*time.Millisecond),
	)
	id, secret := r.resolveSecret("", "", "x", "https://x", nil)
	first := r.Register(id, "https://x", secret)
	time.Sleep(10 * time.Millisecond)
	second := r.Register(id, "https://x", secret)
	assert.True(t, second.After(first), "re-register on the same identity must extend the expiry")
	assert.Len(t, r.Targets(), 1, "still one entry — refresh, not parallel")
}
