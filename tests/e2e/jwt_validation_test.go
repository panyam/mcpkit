package e2e_test

// JWT validation end-to-end tests. These verify that mcpkit's JWTValidator
// correctly validates RS256 JWTs issued by a real oneauth authorization server,
// including claims propagation, expiry checking, issuer/audience enforcement,
// and tampered token rejection.
//
// Each test creates a fresh TestEnv (auth server + MCP server) to ensure isolation.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/oneauth/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_ValidToken_ToolCall verifies the happy path: a valid RS256 JWT with
// correct issuer, audience, and scopes allows a tool call to succeed.
// This is the most basic end-to-end test — if this fails, the auth pipeline
// is fundamentally broken.
func TestE2E_ValidToken_ToolCall(t *testing.T) {
	env := NewTestEnv(t)
	token := env.MintToken(t, "user-1", []string{"tools:read", "tools:call"})

	client := env.ConnectMCPClient(t, token)
	result, err := client.ToolCall("echo", map[string]any{"msg": "hello"})
	require.NoError(t, err)
	assert.Contains(t, result, "hello")
}

// TestE2E_ValidToken_ClaimsPropagation verifies that AuthClaims(ctx) inside a
// tool handler receives the correct subject, issuer, audience, and scopes from
// the JWT. This ensures claims flow from the transport layer through CheckAuth
// → contextWithSession → tool handler context.
func TestE2E_ValidToken_ClaimsPropagation(t *testing.T) {
	env := NewTestEnv(t)
	token := env.MintToken(t, "user-claims-test", []string{"tools:read", "admin:write"})

	client := env.ConnectMCPClient(t, token)
	result, err := client.ToolCall("echo", map[string]any{"msg": "check-claims"})
	require.NoError(t, err)

	// Parse the echo tool's JSON response which includes claims
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &data))

	assert.Equal(t, "user-claims-test", data["sub"])
	assert.Equal(t, env.AS.Issuer(), data["iss"])

	// Audience may be string or []string depending on JWT library
	switch aud := data["aud"].(type) {
	case string:
		assert.Equal(t, env.MCPServerURL, aud)
	case []any:
		assert.Contains(t, aud, env.MCPServerURL)
	}

	scopes, ok := data["scopes"].([]any)
	require.True(t, ok, "scopes should be an array")
	assert.Contains(t, scopes, "tools:read")
	assert.Contains(t, scopes, "admin:write")
}

// TestE2E_ExpiredToken_Rejected verifies that a token with a past expiry time
// is rejected with HTTP 401. This tests the JWTValidator → oneauth APIAuth
// expiry check.
func TestE2E_ExpiredToken_Rejected(t *testing.T) {
	env := NewTestEnv(t)
	token := env.MintExpiredToken(t, "expired-user")

	resp := RawPOST(t, env.MCPServerURL+"/mcp", token, initializeJSON)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestE2E_WrongIssuer_Rejected verifies that a token with an issuer that
// doesn't match the JWTValidator's expected issuer is rejected with HTTP 401.
// This tests the iss claim validation in oneauth's APIAuth.
func TestE2E_WrongIssuer_Rejected(t *testing.T) {
	env := NewTestEnv(t)
	token := env.MintTokenWithIssuer(t, "user-1", "https://evil-issuer.example.com")

	resp := RawPOST(t, env.MCPServerURL+"/mcp", token, initializeJSON)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestE2E_WrongAudience_Rejected verifies that a token with an audience that
// doesn't match the MCP server's URL is rejected with HTTP 401. This tests
// RFC 8707 resource indicator validation.
func TestE2E_WrongAudience_Rejected(t *testing.T) {
	env := NewTestEnv(t)
	token := env.MintTokenWithAudience(t, "user-1", "https://wrong-server.example.com")

	resp := RawPOST(t, env.MCPServerURL+"/mcp", token, initializeJSON)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestE2E_TamperedToken_Rejected verifies that modifying a JWT's payload after
// signing causes signature verification to fail, resulting in HTTP 401. This
// tests the RS256 signature check in the JWKS → JWTValidator pipeline.
func TestE2E_TamperedToken_Rejected(t *testing.T) {
	env := NewTestEnv(t)
	token := env.MintToken(t, "user-1", []string{"tools:read"})

	// Tamper with the payload: decode, modify, re-encode (without re-signing)
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "JWT should have 3 parts")

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	claims["sub"] = "tampered-user"
	modified, _ := json.Marshal(claims)
	parts[1] = base64.RawURLEncoding.EncodeToString(modified)
	tamperedToken := strings.Join(parts, ".")

	resp := RawPOST(t, env.MCPServerURL+"/mcp", tamperedToken, initializeJSON)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestE2E_NoAuthHeader_Rejected verifies that a request without an Authorization
// header is rejected with HTTP 401. This tests the base case of auth enforcement
// — the JWTValidator should not accept anonymous requests.
func TestE2E_NoAuthHeader_Rejected(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawPOST(t, env.MCPServerURL+"/mcp", "", initializeJSON)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestE2E_ClientCredentials_FullFlow verifies the full client_credentials grant
// flow: register an app via the admin API, obtain a token from the HTTP token
// endpoint, and use it to call an MCP tool. This tests the token endpoint →
// JWKS → JWTValidator pipeline with a real HTTP token exchange.
func TestE2E_ClientCredentials_FullFlow(t *testing.T) {
	env := NewTestEnv(t)

	// Register a client app via the admin registration endpoint.
	// The response includes client_id and client_secret.
	regBody := `{"client_domain":"e2e-test-app","signing_alg":"HS256"}`
	regReq, _ := http.NewRequest("POST", env.AS.URL()+"/apps/register", strings.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regReq.Header.Set("X-Admin-Key", env.AS.AdminKey())
	regResp, err := http.DefaultClient.Do(regReq)
	require.NoError(t, err)
	defer regResp.Body.Close()
	require.True(t, regResp.StatusCode == 200 || regResp.StatusCode == 201,
		"app registration should succeed, got %d", regResp.StatusCode)

	var regResult struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	require.NoError(t, json.NewDecoder(regResp.Body).Decode(&regResult))
	require.NotEmpty(t, regResult.ClientID)

	// Get a token via client_credentials grant
	tok := testutil.GetClientCredentialsToken(t,
		env.AS.TokenEndpoint(),
		regResult.ClientID,
		regResult.ClientSecret,
		"tools:read", "tools:call",
	)

	client := env.ConnectMCPClient(t, tok.AccessToken)
	result, err := client.ToolCall("echo", map[string]any{"msg": "from-cc"})
	require.NoError(t, err)
	assert.Contains(t, result, "from-cc")
}
