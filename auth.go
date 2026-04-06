package mcpkit

import (
	"context"
	"net/http"
)

// Claims holds the authenticated identity extracted from a validated request.
// Populated by AuthValidators that also implement ClaimsProvider.
type Claims struct {
	// Subject is the authenticated principal (user ID or client ID).
	Subject string `json:"sub"`

	// Issuer identifies the authorization server that issued the token.
	Issuer string `json:"iss"`

	// Audience lists the intended recipients of the token (RFC 8707).
	Audience []string `json:"aud"`

	// Scopes lists the granted scopes.
	Scopes []string `json:"scope"`

	// Extra holds additional claims not covered by the standard fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// ClaimsProvider is an optional interface for AuthValidators that can extract
// identity claims from a validated request. Called only after Validate succeeds.
//
// Validators that only perform pass/fail checks (like bearerTokenValidator)
// do not need to implement this interface.
type ClaimsProvider interface {
	Claims(r *http.Request) *Claims
}

// AuthClaims returns the authenticated identity from the context, or nil
// if no auth was configured or the validator does not provide claims.
//
// Usage in a tool handler:
//
//	func myHandler(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
//	    claims := mcpkit.AuthClaims(ctx)
//	    if claims != nil {
//	        log.Printf("called by %s", claims.Subject)
//	    }
//	    // ...
//	}
func AuthClaims(ctx context.Context) *Claims {
	sc := sessionFromContext(ctx)
	if sc == nil {
		return nil
	}
	return sc.claims
}

// HasScope checks if the context's authenticated claims include the given scope.
// Returns false if no claims are present.
func HasScope(ctx context.Context, scope string) bool {
	claims := AuthClaims(ctx)
	if claims == nil {
		return false
	}
	for _, s := range claims.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// TokenSource provides access tokens for the MCP client.
// Simple implementations return a static token; OAuth implementations
// handle the full flow including discovery, browser auth, and refresh.
type TokenSource interface {
	// Token returns a valid access token, refreshing if necessary.
	Token() (string, error)
}

// staticTokenSource is a TokenSource that always returns the same token.
type staticTokenSource struct {
	token string
}

func (s *staticTokenSource) Token() (string, error) { return s.token, nil }

// ScopeAwareTokenSource extends TokenSource with scope step-up capability.
// When the server returns 403 with required scopes in the WWW-Authenticate
// header, the client transport calls TokenForScopes to re-authenticate with
// broader permissions.
//
// Implementations that support interactive re-auth (like OAuthTokenSource)
// should implement this interface. Static tokens and implementations that
// cannot acquire new scopes need not implement it — the transport will
// return a ClientAuthError instead of retrying.
type ScopeAwareTokenSource interface {
	TokenSource
	// TokenForScopes invalidates the cached token and triggers a new
	// authorization flow with the given scopes merged into the existing set.
	TokenForScopes(scopes []string) (string, error)
}

// Stability represents the maturity level of an extension.
type Stability string

const (
	// Experimental indicates the extension is in development and may change.
	Experimental Stability = "experimental"
	// Stable indicates the extension is production-ready.
	Stable Stability = "stable"
	// Deprecated indicates the extension will be removed in a future version.
	Deprecated Stability = "deprecated"
)

// Extension describes a protocol extension with maturity metadata.
// Extensions are advertised in the initialize response under capabilities.extensions.
type Extension struct {
	// ID is the extension identifier (e.g., "io.mcpkit/auth").
	ID string `json:"id"`

	// SpecVersion is the version of the spec this extension implements.
	SpecVersion string `json:"specVersion"`

	// Stability indicates the maturity of this extension.
	Stability Stability `json:"stability"`

	// Config holds extension-specific configuration, if any.
	Config map[string]any `json:"config,omitempty"`
}

// ExtensionProvider is implemented by sub-modules to declare their extension.
// This is how mcpkit/auth registers itself without the core module knowing about auth.
type ExtensionProvider interface {
	Extension() Extension
}
