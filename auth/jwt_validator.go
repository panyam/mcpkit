package auth

import (
	"net/http"
	"strings"
	"sync"

	"github.com/panyam/mcpkit"
	"github.com/panyam/oneauth/apiauth"
	"github.com/panyam/oneauth/core"
	"github.com/panyam/oneauth/keys"
)

// JWTValidator validates MCP requests using JWT Bearer tokens.
// It implements mcpkit.AuthValidator and mcpkit.ClaimsProvider by wrapping
// oneauth's APIAuth for JWT signature verification, issuer/audience/scope checks.
//
// Usage:
//
//	validator := auth.NewJWTValidator(auth.JWTConfig{
//	    JWKSURL:             "https://auth.example.com/.well-known/jwks.json",
//	    Issuer:              "https://auth.example.com",
//	    Audience:            "https://mcp.example.com",
//	    ResourceMetadataURL: "https://mcp.example.com/.well-known/oauth-protected-resource/mcp",
//	})
//	srv := mcpkit.NewServer(info, mcpkit.WithAuth(validator))
type JWTValidator struct {
	auth *apiauth.APIAuth
	ks   *keys.JWKSKeyStore

	// ResourceMetadataURL is included in WWW-Authenticate headers on 401 responses.
	ResourceMetadataURL string

	// RequiredScopes are checked on every request. Empty means no global scope requirement.
	RequiredScopes []string

	// AllScopes is included in WWW-Authenticate 401 headers to guide client scope selection.
	// Per spec: clients use this to request scopes upfront, reducing step-up round-trips.
	AllScopes []string

	// recentClaims caches the most recently validated claims by token string.
	// Used by Claims(r) to retrieve claims without re-parsing.
	// A sync.Map is used for concurrent safety across requests.
	recentClaims sync.Map // token string → *mcpkit.Claims
}

// JWTConfig configures a JWTValidator.
type JWTConfig struct {
	// JWKSURL is the authorization server's JWKS endpoint for key discovery.
	JWKSURL string

	// Issuer is the expected "iss" claim in tokens.
	Issuer string

	// Audience is the expected "aud" claim — this MCP server's canonical URI (RFC 8707).
	Audience string

	// ResourceMetadataURL is the URL of this server's Protected Resource Metadata
	// document (RFC 9728). Included in WWW-Authenticate headers.
	ResourceMetadataURL string

	// RequiredScopes are checked on every request (global minimum).
	RequiredScopes []string

	// AllScopes is the complete set of scopes this server supports.
	// Included in WWW-Authenticate 401 headers for client scope selection.
	AllScopes []string
}

// NewJWTValidator creates a JWTValidator backed by oneauth's JWT validation
// and JWKS key discovery.
func NewJWTValidator(cfg JWTConfig) *JWTValidator {
	ks := keys.NewJWKSKeyStore(cfg.JWKSURL)

	auth := &apiauth.APIAuth{
		JWTIssuer:   cfg.Issuer,
		JWTAudience: cfg.Audience,
	}
	// Wire the JWKS key store as the key lookup for JWT verification.
	// APIAuth uses this to resolve kid → public key for signature verification.
	auth.ClientKeyStore = ks

	return &JWTValidator{
		auth:                auth,
		ks:                  ks,
		ResourceMetadataURL: cfg.ResourceMetadataURL,
		RequiredScopes:      cfg.RequiredScopes,
		AllScopes:           cfg.AllScopes,
	}
}

// Validate implements mcpkit.AuthValidator.
// Extracts the Bearer token, validates signature/issuer/audience/expiry via oneauth,
// checks required scopes, and stashes parsed claims for Claims() to read.
func (v *JWTValidator) Validate(r *http.Request) error {
	// Extract Bearer token
	authHeader := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return v.unauthorized("missing or invalid Authorization header")
	}
	token := authHeader[len(prefix):]

	// Validate via oneauth: signature, iss, aud, exp, blacklist
	userID, scopes, customClaims, err := v.auth.ValidateAccessTokenFull(token)
	if err != nil {
		return v.unauthorized("invalid token: " + err.Error())
	}

	// Check required scopes
	if len(v.RequiredScopes) > 0 && !core.ContainsAllScopes(scopes, v.RequiredScopes) {
		return &mcpkit.AuthError{
			Code:            http.StatusForbidden,
			Message:         "insufficient scope",
			WWWAuthenticate: WWWAuth403(v.RequiredScopes...),
		}
	}

	// Build claims and stash in request context
	claims := &mcpkit.Claims{
		Subject: userID,
		Scopes:  scopes,
		Extra:   customClaims,
	}
	if v.auth.JWTIssuer != "" {
		claims.Issuer = v.auth.JWTIssuer
	}
	if v.auth.JWTAudience != "" {
		claims.Audience = []string{v.auth.JWTAudience}
	}

	// Cache claims by token so Claims(r) can retrieve them without re-parsing.
	// This avoids the fragile *r = *r.WithContext(...) pattern.
	v.recentClaims.Store(token, claims)

	return nil
}

// Claims implements mcpkit.ClaimsProvider.
// Returns the claims parsed during the most recent Validate call for the same token.
func (v *JWTValidator) Claims(r *http.Request) *mcpkit.Claims {
	authHeader := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return nil
	}
	token := authHeader[len(prefix):]
	if val, ok := v.recentClaims.LoadAndDelete(token); ok {
		return val.(*mcpkit.Claims)
	}
	return nil
}

// unauthorized returns an AuthError with 401 and a WWW-Authenticate header
// pointing to this server's PRM endpoint.
func (v *JWTValidator) unauthorized(msg string) *mcpkit.AuthError {
	return &mcpkit.AuthError{
		Code:            http.StatusUnauthorized,
		Message:         msg,
		WWWAuthenticate: WWWAuth401(v.ResourceMetadataURL, v.AllScopes...),
	}
}

// Start begins background JWKS key refresh. Call this once at startup.
func (v *JWTValidator) Start() {
	v.ks.Start()
}

// Stop halts background JWKS key refresh.
func (v *JWTValidator) Stop() {
	v.ks.Stop()
}
