package events

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Handler-level tests for γ-2's auth gate + tuple-keyed registry. Tests
// here build a server WITHOUT UnsafeAnonymousPrincipal so they exercise
// the spec-strict path-3 rejection. The shared fixture in
// secret_validation_test.go uses UnsafeAnonymousPrincipal: "test-principal"
// for tests that aren't concerned with the auth gate; this file's tests
// build their own minimal stack so they can vary the auth posture.

// buildAuthGateStack returns a server with the events handlers registered
// + a reference to the registry so tests can inspect target state.
// unsafeAnon controls whether the strict spec auth gate fires:
//   - "" → strict (§"Subscription Identity" → "Authentication required" L361)
//   - non-empty → escape hatch (events.Config.UnsafeAnonymousPrincipal docs)
func buildAuthGateStack(t *testing.T, unsafeAnon string) (*server.Server, *WebhookRegistry) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	webhooks := NewWebhookRegistry()
	Register(Config{
		Sources:                  []EventSource{fakeSecretValidationSource{}},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: unsafeAnon,
	})
	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`0`), Method: "initialize", Params: initParams,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	_, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", Method: "notifications/initialized",
	})
	require.NoError(t, err)
	return srv, webhooks
}

func dispatchSubscribe(t *testing.T, srv *server.Server, params map[string]any) *core.Response {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/subscribe", Params: raw,
	})
	require.NoError(t, err)
	return resp
}

// validSubscribeParams returns a well-formed subscribe request body that
// will pass all validation EXCEPT the auth gate. Use to factor out the
// boilerplate from auth-focused tests.
func validSubscribeParams() map[string]any {
	return map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    "https://example.com/hook",
			"secret": generateSecret(),
		},
	}
}

// TestSubscribe_RejectsAnonymousUnderStrictSpec verifies the spec-strict
// path: when the server has no auth wired AND no UnsafeAnonymousPrincipal
// configured, anonymous webhook subscribes are rejected with -32012
// per §"Subscription Identity" → "Authentication required" L361.
//
// Without this rejection, a misconfigured deployment would silently
// accept anonymous subscribes — the failure mode the spec is preventing.
func TestSubscribe_RejectsAnonymousUnderStrictSpec(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "")
	resp := dispatchSubscribe(t, srv, validSubscribeParams())

	require.NotNil(t, resp.Error, "expected -32012; got success result")
	assert.Equal(t, ErrCodeUnauthorized, resp.Error.Code,
		"spec-mandated -32012 Unauthorized for anonymous webhook subscribe")
}

// TestSubscribe_AcceptsAnonymousWithEscape verifies the demo escape
// hatch: when UnsafeAnonymousPrincipal is set, anonymous calls succeed
// using the configured principal as claims.Subject's stand-in.
func TestSubscribe_AcceptsAnonymousWithEscape(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	resp := dispatchSubscribe(t, srv, validSubscribeParams())

	require.Nil(t, resp.Error,
		"with UnsafeAnonymousPrincipal set, anonymous subscribes must succeed; got %+v", resp.Error)
	require.NotNil(t, resp.Result)
}

// TestSubscribe_TupleIdempotence verifies the spec's idempotent-subscribe
// rule (§"Subscription Identity" → "Key composition" L363): two subscribe
// calls with identical (name, params, delivery.url) AND same principal
// are the SAME subscription — the second is a TTL refresh, not a new
// registry entry.
func TestSubscribe_TupleIdempotence(t *testing.T) {
	srv, webhooks := buildAuthGateStack(t, "test-principal")

	params := validSubscribeParams()
	resp1 := dispatchSubscribe(t, srv, params)
	require.Nil(t, resp1.Error)
	resp2 := dispatchSubscribe(t, srv, params)
	require.Nil(t, resp2.Error)

	// Same canonical key → same registry entry → exactly one target.
	assert.Len(t, webhooks.Targets(), 1, "two subscribes with same tuple must produce ONE registry entry (idempotent refresh)")

	// Both responses carry the SAME server-derived id.
	id1 := extractIDField(t, resp1)
	id2 := extractIDField(t, resp2)
	assert.Equal(t, id1, id2, "both subscribes must derive the same id (deterministic over canonical key)")
}

// TestSubscribe_TupleIsolationCrossPrincipal verifies the spec's
// cross-tenant isolation property (§"Subscription Identity" L378): two
// subscribes with same (name, params, delivery.url) but DIFFERENT
// principals are DIFFERENT subscriptions. We can't easily set different
// principals on Dispatch (no auth middleware in test fixture), so this
// test exercises the property at the canonical-key level instead —
// which is what the registry compares.
func TestSubscribe_TupleIsolationCrossPrincipal(t *testing.T) {
	webhooks := NewWebhookRegistry()
	keyAlice := canonicalKey("alice", "https://example.com/hook", "fake.event", nil)
	keyBob := canonicalKey("bob", "https://example.com/hook", "fake.event", nil)
	idAlice := deriveSubscriptionID(keyAlice)
	idBob := deriveSubscriptionID(keyBob)

	// Same URL, name, params; different principal → distinct registry entries.
	webhooks.Register(keyAlice, idAlice, "https://example.com/hook", "whsec_a")
	webhooks.Register(keyBob, idBob, "https://example.com/hook", "whsec_b")

	assert.Len(t, webhooks.Targets(), 2, "different principals must produce distinct registry entries")
	assert.NotEqual(t, idAlice, idBob, "different canonical keys must derive different ids")

	// Unregistering alice's tuple leaves bob's intact (no cross-tenant collateral).
	webhooks.Unregister(keyAlice)
	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.Equal(t, idBob, targets[0].ID, "unregister(alice's tuple) must not affect bob's subscription")
}

// TestSubscribe_RejectsClientSuppliedID verifies γ-3's wire-strict
// rejection of legacy id-bearing subscribe requests. Per spec
// §"Subscription Identity" → "Key composition" L363: "There is no
// client-generated id — a subscription is fully determined by what it
// listens for, where it delivers, and who asked." Old SDKs sending an
// id field get a loud -32602 instead of a silent mis-keying that
// would route deliveries under the wrong subscription.
func TestSubscribe_RejectsClientSuppliedID(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal")
	params := validSubscribeParams()
	params["id"] = "client-picked-id" // pre-γ wire shape

	resp := dispatchSubscribe(t, srv, params)
	require.NotNil(t, resp.Error, "client-supplied id must be rejected")
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code, "expected -32602 InvalidParams")
	assert.Contains(t, resp.Error.Message, "id is not accepted",
		"error message should explain why; got %q", resp.Error.Message)
}

// TestUnsubscribe_ByTuple verifies the spec's unsubscribe-by-tuple
// behavior (§"Unsubscribing: events/unsubscribe" L509): client supplies
// (name, params, delivery.url); server resolves via canonical key,
// removes the entry. No id required in the request.
func TestUnsubscribe_ByTuple(t *testing.T) {
	srv, webhooks := buildAuthGateStack(t, "test-principal")

	subParams := validSubscribeParams()
	subResp := dispatchSubscribe(t, srv, subParams)
	require.Nil(t, subResp.Error)
	require.Len(t, webhooks.Targets(), 1)

	// Unsubscribe with the same (name, delivery.url) — no id field.
	unsubParams := map[string]any{
		"name":     subParams["name"],
		"delivery": map[string]any{"url": subParams["delivery"].(map[string]any)["url"]},
	}
	rawUnsub, err := json.Marshal(unsubParams)
	require.NoError(t, err)
	unsubResp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "events/unsubscribe", Params: rawUnsub,
	})
	require.NoError(t, err)
	require.Nil(t, unsubResp.Error)
	assert.Empty(t, webhooks.Targets(), "tuple-form unsubscribe must remove the matching entry")
}

// --- helpers ---

// extractIDField reads the "id" field from a Response.Result map.
func extractIDField(t *testing.T, resp *core.Response) string {
	t.Helper()
	m, ok := resp.Result.(map[string]any)
	require.True(t, ok, "response.Result must be map[string]any; got %T", resp.Result)
	id, ok := m["id"].(string)
	require.True(t, ok, "response.Result[\"id\"] must be string; got %T", m["id"])
	return id
}
