package auth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMountAuthAutoWiresAllScopes verifies that when MountAuth is called
// with a JWTValidator in AuthConfig, the validator's AllScopes field is
// automatically populated from ScopesSupported. This ensures 401
// WWW-Authenticate responses advertise all scopes the server supports,
// allowing clients to request broad scopes upfront and avoid step-up
// round-trips (#50).
func TestMountAuthAutoWiresAllScopes(t *testing.T) {
	validator := NewJWTValidator(JWTConfig{
		JWKSURL:  "https://auth.example.com/.well-known/jwks.json",
		Issuer:   "https://auth.example.com",
		Audience: "https://mcp.example.com",
		// Note: AllScopes intentionally NOT set — should be auto-populated
	})

	scopes := []string{"tools:read", "tools:call", "admin:write"}
	mux := http.NewServeMux()
	MountAuth(mux, AuthConfig{
		ResourceURI:          "https://mcp.example.com",
		AuthorizationServers: []string{"https://auth.example.com"},
		ScopesSupported:      scopes,
		MCPPath:              "/mcp",
		Validator:            validator,
	})

	assert.Equal(t, scopes, validator.AllScopes,
		"MountAuth should auto-wire ScopesSupported into validator.AllScopes")
}

// TestMountAuthDoesNotOverrideExplicitAllScopes verifies that if the
// caller has already set AllScopes explicitly on the validator, MountAuth
// does not overwrite it. This lets advanced users configure a different
// scope set for 401 advertisement than for PRM.
func TestMountAuthDoesNotOverrideExplicitAllScopes(t *testing.T) {
	explicitScopes := []string{"only:this"}
	validator := NewJWTValidator(JWTConfig{
		JWKSURL:   "https://auth.example.com/.well-known/jwks.json",
		Issuer:    "https://auth.example.com",
		Audience:  "https://mcp.example.com",
		AllScopes: explicitScopes, // explicitly set
	})

	mux := http.NewServeMux()
	MountAuth(mux, AuthConfig{
		ResourceURI:          "https://mcp.example.com",
		AuthorizationServers: []string{"https://auth.example.com"},
		ScopesSupported:      []string{"tools:read", "tools:call", "admin:write"},
		MCPPath:              "/mcp",
		Validator:            validator,
	})

	assert.Equal(t, explicitScopes, validator.AllScopes,
		"MountAuth should not override explicit AllScopes")
}

// TestMountAuthNilValidator verifies that MountAuth works correctly when
// no validator is provided (backward compatibility with existing callers).
func TestMountAuthNilValidator(t *testing.T) {
	mux := http.NewServeMux()
	MountAuth(mux, AuthConfig{
		ResourceURI:          "https://mcp.example.com",
		AuthorizationServers: []string{"https://auth.example.com"},
		ScopesSupported:      []string{"tools:read"},
		MCPPath:              "/mcp",
		// No Validator — should not panic
	})
}
