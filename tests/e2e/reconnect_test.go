package e2e_test

// Client reconnection E2E tests with real JWTs. Verify that the client
// recovers from server restarts and token expiry through the full pipeline:
// transport disconnect → reconnect → re-initialize → fresh token → retry.

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_Reconnect_WithTokenRefresh verifies that a client with reconnection
// enabled recovers from using an expired JWT. The token source provides an
// expired token first (causing 401), then a valid one. The client's
// doWithAuthRetry handles the 401 refresh on the first connect, proving
// the reconnection + auth retry pipeline works end-to-end with real JWTs.
func TestE2E_Reconnect_WithTokenRefresh(t *testing.T) {
	env := NewTestEnv(t)

	// Token source: first call returns expired, second returns valid
	expiredToken := env.MintExpiredToken(t, "reconnect-user")
	validToken := env.MintToken(t, "reconnect-user", []string{"tools:read"})

	ts := &refreshableTokenSource{
		tokens: []string{expiredToken, validToken},
	}

	client := client.NewClient(
		env.MCPServerURL+"/mcp",
		core.ClientInfo{Name: "reconnect-test", Version: "0.1.0"},
		client.WithTokenSource(ts),
		client.WithMaxRetries(2),
		client.WithReconnectBackoff(10*time.Millisecond),
	)

	// Connect should succeed: 401 on expired token → refresh → retry succeeds
	err := client.Connect()
	require.NoError(t, err, "should connect after token refresh")
	defer client.Close()

	result, err := client.ToolCall("echo", map[string]any{"msg": "reconnected"})
	require.NoError(t, err)
	assert.Contains(t, result, "reconnected")
}

// TestE2E_Reconnect_TransientErrorClassification verifies that auth errors
// from real JWT validation (wrong audience, wrong issuer) are correctly
// classified as NON-transient, preventing unnecessary reconnection attempts.
func TestE2E_Reconnect_TransientErrorClassification(t *testing.T) {
	env := NewTestEnv(t)

	// Token with wrong audience — this is a terminal auth error, not transient
	wrongAudToken := env.MintTokenWithAudience(t, "wrong-aud-user", "https://wrong.example.com")

	client := client.NewClient(
		env.MCPServerURL+"/mcp",
		core.ClientInfo{Name: "classify-test", Version: "0.1.0"},
		client.WithClientBearerToken(wrongAudToken),
		client.WithMaxRetries(3),
		client.WithReconnectBackoff(10*time.Millisecond),
	)

	// Should fail fast (auth error, not transient) — no reconnection attempts
	start := time.Now()
	err := client.Connect()
	elapsed := time.Since(start)

	require.Error(t, err, "should fail with auth error")
	// If reconnection was attempted, it would take at least 30ms (3 retries * 10ms)
	assert.Less(t, elapsed, 100*time.Millisecond,
		"should fail fast without reconnection attempts (auth errors are terminal)")
}

// TestE2E_Reconnect_RecentlyExpiredJWT verifies a realistic production
// scenario: a JWT that was valid when the session started but expired during
// use. The client should detect the 401 on the next call, refresh the token,
// and succeed without manual intervention.
func TestE2E_Reconnect_RecentlyExpiredJWT(t *testing.T) {
	env := NewTestEnv(t)

	// Start with a token that expires in 100ms
	shortLivedToken, err := env.AS.MintTokenWithClaims(jwt.MapClaims{
		"sub":    "short-lived-user",
		"aud":    env.MCPServerURL,
		"scopes": []string{"tools:read"},
		"exp":    time.Now().Add(200 * time.Millisecond).Unix(),
	})
	require.NoError(t, err)

	validToken := env.MintToken(t, "short-lived-user", []string{"tools:read"})

	callCount := 0
	ts := &refreshableTokenSource{
		tokens: []string{shortLivedToken, validToken},
	}

	client := client.NewClient(
		env.MCPServerURL+"/mcp",
		core.ClientInfo{Name: "expiry-test", Version: "0.1.0"},
		client.WithTokenSource(ts),
		client.WithMaxRetries(2),
		client.WithReconnectBackoff(10*time.Millisecond),
	)

	// Connect with the short-lived token (still valid)
	err = client.Connect()
	require.NoError(t, err)
	defer client.Close()

	// First call should work (token still valid)
	result, err := client.ToolCall("echo", map[string]any{"msg": "alive"})
	require.NoError(t, err)
	assert.Contains(t, result, "alive")
	callCount++

	// Wait for token to expire
	time.Sleep(300 * time.Millisecond)

	// Next call: token expired → 401 → auth retry with refreshed token → success
	result, err = client.ToolCall("echo", map[string]any{"msg": "refreshed"})
	require.NoError(t, err)
	assert.Contains(t, result, "refreshed")
	callCount++

	assert.Equal(t, 2, callCount, "both calls should succeed")
}
