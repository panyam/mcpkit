package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func getPRM(t *testing.T, mux *http.ServeMux, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://mcp.example.com"+path, nil))
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	return rec.Code, doc
}

// RFC 9728 §3.3: a client discards a document whose resource differs from
// the one it built the well-known URL from, so a pathed resource's metadata
// must not also answer at the root URL (#1732, oneauth PR 524).
func TestMountAuthPathedResourceServesPRMOnlyAtPathURL(t *testing.T) {
	mux := http.NewServeMux()
	MountAuth(mux, AuthConfig{
		ResourceURI:          "https://mcp.example.com/mcp",
		AuthorizationServers: []string{"https://auth.example.com"},
		MCPPath:              "/mcp",
	})

	code, doc := getPRM(t, mux, "/.well-known/oauth-protected-resource/mcp")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "https://mcp.example.com/mcp", doc["resource"])

	code, _ = getPRM(t, mux, "/.well-known/oauth-protected-resource")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestMountAuthRootResourceServesPRMAtRootURL(t *testing.T) {
	mux := http.NewServeMux()
	MountAuth(mux, AuthConfig{
		ResourceURI:          "https://mcp.example.com",
		AuthorizationServers: []string{"https://auth.example.com"},
	})

	code, doc := getPRM(t, mux, "/.well-known/oauth-protected-resource")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "https://mcp.example.com", doc["resource"])
}
