package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStringArrayClaim covers the array-shaped scope claim parsing
// (oneauth "scopes", Okta/Azure/Entra "scp").
func TestStringArrayClaim(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want []string
	}{
		{"nil", nil, nil},
		{"string not array", "read write", nil},
		{"array of strings", []any{"read", "write"}, []string{"read", "write"}},
		{"empty array", []any{}, []string{}},
		{"mixed types skips non-strings", []any{"read", 42, "write"}, []string{"read", "write"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stringArrayClaim(tc.raw))
		})
	}
}

// TestScopeStr covers the space-delimited string scope claim
// (Keycloak/RFC 6749 "scope", some IdPs' "scp").
func TestScopeStr(t *testing.T) {
	assert.Equal(t, "read write", scopeStr("read write"))
	assert.Equal(t, "", scopeStr(nil))
	assert.Equal(t, "", scopeStr([]any{"read"}))
}

// TestJWTValidator_ScopeClaimShapes proves the validator extracts scopes from
// every IdP claim format end-to-end (signed token -> JWKS -> RequiredScopes
// gate). The Okta "scp" array is the case that motivated the change: without
// it an Okta-issued token parses to zero scopes and every gated call 403s.
func TestJWTValidator_ScopeClaimShapes(t *testing.T) {
	cases := []struct {
		name       string
		claimSetup func(jwt.MapClaims)
	}{
		{"scopes array (oneauth)", func(c jwt.MapClaims) { c["scopes"] = []string{"admin-write"} }},
		{"scp array (Okta/Entra)", func(c jwt.MapClaims) { c["scp"] = []string{"admin-write"} }},
		{"scope string (Keycloak)", func(c jwt.MapClaims) { c["scope"] = "tools-read admin-write" }},
		{"scp string", func(c jwt.MapClaims) { c["scp"] = "admin-write" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jwksURL, issuer, token, cleanup := setupTestJWKSClaims(t, func(c jwt.MapClaims) {
				c["sub"] = "alice"
				tc.claimSetup(c)
			})
			defer cleanup()

			v := NewJWTValidator(JWTConfig{
				JWKSURL:        jwksURL,
				Issuer:         issuer,
				Audience:       "https://mcp.test",
				RequiredScopes: []string{"admin-write"},
			})
			r := requestWithActiveSpan(token, nil)
			// A token carrying admin-write in any claim shape must satisfy the
			// RequiredScopes gate — i.e. Validate returns no error.
			require.NoError(t, v.Validate(r), "admin-write should satisfy the gate for %s", tc.name)

			claims := v.Claims(r)
			require.NotNil(t, claims)
			assert.Contains(t, claims.Scopes, "admin-write")
		})
	}
}

// TestJWTValidator_ScopeClaimPrecedence documents that "scopes" wins over
// "scp" when both are present, matching the extraction order.
func TestJWTValidator_ScopeClaimPrecedence(t *testing.T) {
	jwksURL, issuer, token, cleanup := setupTestJWKSClaims(t, func(c jwt.MapClaims) {
		c["sub"] = "alice"
		c["scopes"] = []string{"admin-write"}
		c["scp"] = []string{"tools-read"}
	})
	defer cleanup()

	v := NewJWTValidator(JWTConfig{JWKSURL: jwksURL, Issuer: issuer, Audience: "https://mcp.test"})
	r := requestWithActiveSpan(token, nil)
	require.NoError(t, v.Validate(r))

	claims := v.Claims(r)
	require.NotNil(t, claims)
	assert.Equal(t, []string{"admin-write"}, claims.Scopes, "scopes claim takes precedence over scp")
}
