package events

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateClientSecret_HappyPath verifies a freshly-generated
// SDK-shaped secret passes validation. Without this, the SDK and the
// validator would disagree about what "valid" means and any auto-generated
// secret would be rejected at subscribe time.
func TestValidateClientSecret_HappyPath(t *testing.T) {
	s := generateSecret()
	assert.NoError(t, validateClientSecret(s),
		"generateSecret output must satisfy validateClientSecret")
}

// TestValidateClientSecret_RejectsMissingPrefix pins the spec contract that
// the value MUST start with "whsec_". A receiver-side library that keys on
// the prefix would silently fail to verify a delivery signed with a
// prefix-less secret.
func TestValidateClientSecret_RejectsMissingPrefix(t *testing.T) {
	err := validateClientSecret("just-a-random-string")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whsec_")
}

// TestValidateClientSecret_RejectsPrefixOnly catches the trivial case
// where the caller passes the literal "whsec_" (or "whsec_") with no
// random material — would HMAC-sign every delivery with a known empty
// key, making signatures forgeable.
func TestValidateClientSecret_RejectsPrefixOnly(t *testing.T) {
	err := validateClientSecret("whsec_")
	require.Error(t, err)
}

// TestValidateClientSecret_RejectsTooShort verifies the spec's 24-byte
// floor. A secret with too few random bytes provides insufficient
// entropy and is brute-forceable; the spec mandates rejection at
// subscribe time so the secret never reaches the signing path.
func TestValidateClientSecret_RejectsTooShort(t *testing.T) {
	// 23 bytes — one byte under the 24-byte floor.
	var raw [23]byte
	_, _ = rand.Read(raw[:])
	short := webhookSecretPrefix + base64.StdEncoding.EncodeToString(raw[:])
	err := validateClientSecret(short)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "24")
}

// TestValidateClientSecret_RejectsTooLong verifies the spec's 64-byte
// ceiling. Past 64 raw bytes the secret is no stronger but consumes
// disproportionate gateway / receiver storage; rejection keeps the
// envelope predictable.
func TestValidateClientSecret_RejectsTooLong(t *testing.T) {
	// 65 bytes — one byte over the 64-byte ceiling.
	var raw [65]byte
	_, _ = rand.Read(raw[:])
	long := webhookSecretPrefix + base64.StdEncoding.EncodeToString(raw[:])
	err := validateClientSecret(long)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "64")
}

// TestValidateClientSecret_RejectsNonBase64 verifies the portion after
// whsec_ must be base64-encoded. Garbage bytes there would not decode to
// a usable HMAC key and should fail-fast at subscribe time.
func TestValidateClientSecret_RejectsNonBase64(t *testing.T) {
	err := validateClientSecret("whsec_not!valid!base64!material")
	require.Error(t, err)
}

// TestValidateClientSecret_AcceptsBothBase64Variants ensures clients can
// emit either standard base64 (with padding) or raw URL-safe base64
// (without padding) — the SDKs in the wild don't agree on which they
// produce, and both are spec-conformant.
func TestValidateClientSecret_AcceptsBothBase64Variants(t *testing.T) {
	var raw [32]byte
	_, _ = rand.Read(raw[:])

	std := webhookSecretPrefix + base64.StdEncoding.EncodeToString(raw[:])
	rawURL := webhookSecretPrefix + base64.RawURLEncoding.EncodeToString(raw[:])

	assert.NoError(t, validateClientSecret(std), "standard base64 (with padding) should validate")
	assert.NoError(t, validateClientSecret(rawURL), "raw URL-safe base64 (no padding) should validate")
}

// --- Handler-level red-before-green tests for the subscribe path ---
//
// These exercise the events/subscribe handler end-to-end via direct
// invocation of the generated subscribe-response struct shape (the
// path the JSON-RPC handler takes after validation passes). Failing
// tests means the handler accepts a malformed secret that would
// produce unverifiable deliveries — a real wire-conformance regression.

// TestSubscribe_RejectsMissingSecret verifies the subscribe handler
// rejects requests with no delivery.secret. Per the spec, the secret
// is REQUIRED — there is no server-side fallback.
func TestSubscribe_RejectsMissingSecret(t *testing.T) {
	resp := callSubscribeHandler(t, map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode": "webhook",
			"url":  "https://example.com/hook",
			// no secret
		},
	})
	requireRPCError(t, resp, core.ErrCodeInvalidParams, "delivery.secret")
}

// TestSubscribe_RejectsMalformedSecret verifies the handler rejects
// secrets that don't match the spec's whsec_ + base64-of-24-to-64-bytes
// format. A subscription with such a secret could never produce
// verifiable deliveries; better to fail at subscribe.
func TestSubscribe_RejectsMalformedSecret(t *testing.T) {
	bad := []string{
		"not-a-secret",                // no prefix
		"whsec_",                      // prefix only
		"whsec_too-short",             // too few bytes after decoding
		"whsec_!!!!not-base64!!!!",    // non-base64 garbage
	}
	for _, s := range bad {
		t.Run(s, func(t *testing.T) {
			resp := callSubscribeHandler(t, map[string]any{
				"name": "fake.event",
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    "https://example.com/hook",
					"secret": s,
				},
			})
			requireRPCError(t, resp, core.ErrCodeInvalidParams, "secret")
		})
	}
}

// TestSubscribe_AcceptsValidWhsecSecret happy-path counter-test:
// a properly-formatted client-supplied secret should result in a
// successful subscribe. This catches over-eager validators that
// reject conformant inputs.
func TestSubscribe_AcceptsValidWhsecSecret(t *testing.T) {
	resp := callSubscribeHandler(t, map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    "https://example.com/hook",
			"secret": generateSecret(),
		},
	})
	require.Nil(t, resp.Error, "valid whsec_ secret must be accepted")
	require.NotNil(t, resp.Result)
}

// TestSubscribe_ResponseDoesNotEchoSecret pins the spec contract that
// the events/subscribe response MUST NOT carry the secret. The client
// supplied it; echoing risks leaking via logs / proxies / IDE network
// panes during development. Failing this test means we are leaking the
// signing secret unnecessarily on the response leg.
func TestSubscribe_ResponseDoesNotEchoSecret(t *testing.T) {
	supplied := generateSecret()
	resp := callSubscribeHandler(t, map[string]any{
		"name": "fake.event",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    "https://example.com/hook",
			"secret": supplied,
		},
	})
	require.Nil(t, resp.Error)

	// Marshal the response and assert the raw bytes contain neither
	// "secret" nor the supplied whsec_ value.
	raw, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	body := string(raw)
	assert.NotContains(t, body, `"secret"`, "subscribe response must not include a secret field")
	assert.NotContains(t, body, supplied, "subscribe response must not echo the client-supplied secret value")
}

// TestUnsubscribe_RequiresTupleNotSecret verifies the handler ignores
// the legacy proof-of-possession secret-form unsubscribe — γ keys
// unsubscribe on the (principal, name, params, delivery.url) tuple
// per spec §"Unsubscribing: events/unsubscribe" L509. A request that
// supplies only delivery.url + delivery.secret (no name) is rejected
// with name-required because the secret field is no longer part of the
// unsubscribe surface.
func TestUnsubscribe_RequiresTupleNotSecret(t *testing.T) {
	resp := callUnsubscribeHandler(t, map[string]any{
		// name intentionally omitted; secret would have worked pre-β
		"delivery": map[string]any{
			"url":    "https://example.com/hook",
			"secret": "whsec_should-not-be-accepted-here",
		},
	})
	requireRPCError(t, resp, core.ErrCodeInvalidParams, "name")
}

// --- handler-invocation helpers (private to test file) ---

// fakeSecretValidationSource is the bare minimum EventSource the
// subscribe handler needs to look up "fake.event" by name. Doesn't
// produce any events.
type fakeSecretValidationSource struct{}

func (fakeSecretValidationSource) Def() EventDef {
	return EventDef{Name: "fake.event"}
}
func (fakeSecretValidationSource) Poll(_ string, _ int) PollResult { return PollResult{} }
func (fakeSecretValidationSource) Latest() string                  { return "" }

// buildSecretValidationStack returns a server with the events handlers
// registered (with UnsafeAnonymousPrincipal: "test-principal" so the
// handlers don't reject every test request with -32012 Unauthorized —
// γ adds spec-mandated auth gating, but the secret-validation tests
// here are concerned with the validator and unsubscribe shape, not
// the auth gate. Auth-specific tests live in identity_handler_test.go
// (γ-2 follow-on).) The initialize handshake is completed so subsequent
// Dispatch calls accept arbitrary methods.
func buildSecretValidationStack(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{fakeSecretValidationSource{}},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	// Two-step init handshake — the dispatcher rejects non-init methods
	// until BOTH (a) the initialize request returns and (b) the client
	// sends the notifications/initialized notification.
	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`0`),
		Method:  "initialize",
		Params:  initParams,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "initialize should succeed; got %+v", resp.Error)

	// Notification — no id, no expected response
	_, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	require.NoError(t, err)
	return srv
}

// callSubscribeHandler invokes the events/subscribe handler with the
// given params via the server's normal dispatch path.
func callSubscribeHandler(t *testing.T, params map[string]any) *core.Response {
	t.Helper()
	srv := buildSecretValidationStack(t)
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "events/subscribe",
		Params:  raw,
	})
	require.NoError(t, err)
	return resp
}

func callUnsubscribeHandler(t *testing.T, params map[string]any) *core.Response {
	t.Helper()
	srv := buildSecretValidationStack(t)
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "events/unsubscribe",
		Params:  raw,
	})
	require.NoError(t, err)
	return resp
}

func requireRPCError(t *testing.T, resp *core.Response, wantCode int, mustContain string) {
	t.Helper()
	require.NotNil(t, resp.Error, "expected JSON-RPC error response, got result=%v", resp.Result)
	assert.Equal(t, wantCode, resp.Error.Code)
	if mustContain != "" {
		assert.True(t, strings.Contains(resp.Error.Message, mustContain),
			"error message %q should contain %q", resp.Error.Message, mustContain)
	}
}
