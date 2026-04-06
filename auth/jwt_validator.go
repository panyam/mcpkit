package auth

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/golang-jwt/jwt/v5"
	"github.com/panyam/mcpkit"
	"github.com/panyam/oneauth/apiauth"
	"github.com/panyam/oneauth/core"
	"github.com/panyam/oneauth/keys"
	"github.com/panyam/oneauth/utils"
)

// JWTValidator validates MCP requests using JWT Bearer tokens.
// It implements mcpkit.AuthValidator and mcpkit.ClaimsProvider by wrapping
// oneauth's APIMiddleware for JWKS-based JWT signature verification, and
// APIAuth for issuer/audience/scope checks.
//
// Uses APIMiddleware (not APIAuth.ValidateAccessTokenFull) because the middleware
// supports kid-based key lookup from a KeyStore (including JWKSKeyStore), while
// APIAuth's jwtKeyFunc only supports fixed symmetric/asymmetric keys.
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

	// Validate JWT with kid-based JWKS key lookup. We use jwt.Parse directly
	// with a custom keyfunc because APIAuth.ValidateAccessTokenFull only supports
	// fixed keys, not kid-based KeyStore lookup needed for JWKS.
	parsed, err := jwt.Parse(token, v.jwksKeyFunc)
	if err != nil {
		return v.unauthorized("invalid token: " + err.Error())
	}
	if !parsed.Valid {
		return v.unauthorized("invalid token")
	}

	mapClaims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return v.unauthorized("invalid claims")
	}

	// Verify issuer
	if v.auth.JWTIssuer != "" {
		if iss, _ := mapClaims["iss"].(string); iss != v.auth.JWTIssuer {
			return v.unauthorized("invalid issuer")
		}
	}

	// Verify audience (RFC 8707 resource indicator)
	if v.auth.JWTAudience != "" {
		audOK := false
		switch aud := mapClaims["aud"].(type) {
		case string:
			audOK = aud == v.auth.JWTAudience
		case []any:
			for _, a := range aud {
				if s, ok := a.(string); ok && s == v.auth.JWTAudience {
					audOK = true
					break
				}
			}
		}
		if !audOK {
			return v.unauthorized(fmt.Sprintf("invalid audience: expected %q", v.auth.JWTAudience))
		}
	}

	// Extract user ID
	userID, _ := mapClaims["sub"].(string)
	if userID == "" {
		return v.unauthorized("missing subject")
	}

	// Extract scopes — handle both formats:
	//   "scopes": ["read", "write"]  (oneauth array format)
	//   "scope": "read write"        (Keycloak/RFC 6749 space-delimited string)
	var scopes []string
	if scopesRaw, ok := mapClaims["scopes"].([]any); ok {
		for _, s := range scopesRaw {
			if str, ok := s.(string); ok {
				scopes = append(scopes, str)
			}
		}
	} else if scopeStr, ok := mapClaims["scope"].(string); ok && scopeStr != "" {
		scopes = strings.Fields(scopeStr)
	}

	// Check required scopes
	if len(v.RequiredScopes) > 0 && !core.ContainsAllScopes(scopes, v.RequiredScopes) {
		return &mcpkit.AuthError{
			Code:            http.StatusForbidden,
			Message:         "insufficient scope",
			WWWAuthenticate: WWWAuth403(v.RequiredScopes...),
		}
	}

	// Extract custom claims
	standardClaims := map[string]bool{
		"sub": true, "iss": true, "aud": true, "exp": true,
		"iat": true, "nbf": true, "jti": true, "type": true, "scopes": true,
	}
	customClaims := make(map[string]any)
	for k, v := range mapClaims {
		if !standardClaims[k] {
			customClaims[k] = v
		}
	}

	// Build claims
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

// jwksKeyFunc resolves the verification key for a JWT by looking up the kid
// header in the JWKS key store. This enables RS256/ES256 token verification
// via dynamically fetched JWKS keys.
func (v *JWTValidator) jwksKeyFunc(token *jwt.Token) (any, error) {
	kid, ok := token.Header["kid"].(string)
	if !ok || kid == "" {
		return nil, fmt.Errorf("missing kid header")
	}
	rec, err := v.ks.GetKeyByKid(kid)
	if err != nil {
		return nil, fmt.Errorf("key not found for kid %q: %w", kid, err)
	}
	alg, _ := token.Header["alg"].(string)
	if alg != rec.Algorithm {
		return nil, fmt.Errorf("algorithm mismatch: token has %s, key expects %s", alg, rec.Algorithm)
	}
	return utils.DecodeVerifyKey(rec.Key, rec.Algorithm)
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
