package core

import (
	"context"
	"net/http"
)

// Claims holds the authenticated identity extracted from a validated request.
// Populated by AuthValidators that also implement ClaimsProvider.
//
// Subject vs Tenant: Subject is the raw OAuth subject (the token's `sub`
// claim). Tenant is the multi-tenancy partition the subject belongs to,
// when the validator can derive one — for Keycloak, the realm name from
// the issuer URL. Single-tenant deployments leave Tenant empty.
//
// Consumers that need a string-form identity (session-binding, webhook
// canonical-key construction, audit logs that mention "who") should call
// the ext/auth helper auth.PrincipalFor(claims) rather than concatenating
// Tenant + Subject themselves — the encoding rule (separator character,
// empty-tenant fallback) lives in one place.
type Claims struct {
	// Subject is the authenticated principal's raw OAuth subject — the
	// token's `sub` claim, unmodified. Use auth.PrincipalFor(claims) for
	// a tenant-aware string identity.
	Subject string `json:"sub"`

	// SessionID is the OIDC `sid` claim (RFC 8417 / OIDC core § 2),
	// when present. Populated by validators that can extract it from
	// the token — JWTValidator reads `sid` directly from MapClaims;
	// IntrospectionValidator pulls it from the bearer JWT payload
	// without re-verifying (introspection already vouched). Empty
	// when the token carries no sid (legacy issuers, opaque tokens,
	// non-OIDC ASes).
	//
	// Consumed by Back-Channel Logout fan-out (panyam/mcpkit issue
	// 709) so a single AS-revoked session can be matched against
	// any application-level state keyed on it (webhook subscriptions,
	// long-running streams, cached lookups).
	SessionID string `json:"sid,omitempty"`

	// Tenant is the multi-tenancy partition the subject belongs to.
	// Empty for single-tenant deployments or validators that don't
	// expose a tenant concept. Populated by the IntrospectionValidator
	// from the introspection response's iss claim (Keycloak realm) and
	// by future tenant-aware validators.
	Tenant string `json:"tenant,omitempty"`

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
	if sc := sessionFromContext(ctx); sc != nil && sc.claims != nil {
		return sc.claims
	}
	return statelessClaimsFromContext(ctx)
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

// GetScopes returns the scope set granted to the authenticated caller, or nil
// when no claims are attached to ctx. Consistent with HasScope, absent claims
// and present-but-empty scopes both yield nil — callers that need to
// distinguish "unauthenticated" from "authenticated but no scopes" must reach
// for AuthClaims directly.
func GetScopes(ctx context.Context) []string {
	claims := AuthClaims(ctx)
	if claims == nil {
		return nil
	}
	return claims.Scopes
}

// AuthValidator validates an HTTP request and returns claims on success.
type AuthValidator interface {
	Validate(r *http.Request) error
}

// AuthError is returned when authentication fails.
type AuthError struct {
	Code            int
	Message         string
	WWWAuthenticate string // optional WWW-Authenticate header value
}

func (e *AuthError) Error() string { return e.Message }

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

// InvalidatingTokenSource extends TokenSource with an explicit
// cache-invalidation hook. Retry layers call Invalidate before
// re-calling Token to force the source to re-run discovery and any
// client-credential resolution — necessary when the upstream
// authorization server has changed (SEP-2352) or when a previously
// cached token has been rejected with 401.
//
// Without this hook, a TokenSource that caches an authInfo / DCR
// credentials pair will keep handing back the same stale token on
// every retry, defeating the retry. With it, the source has a defined
// moment to drop cached state.
//
// Optional — implementations that have no internal cache (static
// tokens, the simple Bearer wrapper) need not implement it.
// DoWithAuthRetry checks for the interface via type assertion.
type InvalidatingTokenSource interface {
	TokenSource
	// Invalidate drops cached discovery, client credentials, and any
	// cached access token. The next Token call MUST re-run the full
	// flow. Safe to call multiple times — implementations clear what
	// they have and otherwise no-op.
	Invalidate()
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

// RefValidator is an optional interface that ExtensionProviders can implement
// to validate tool-to-resource references at server startup. The server calls
// ValidateRefs for each extension that implements this interface, passing all
// registered tools and the URIs of registered resources and templates.
// Returns a list of human-readable warning messages (empty if all refs resolve).
type RefValidator interface {
	ValidateRefs(tools []ToolDef, resourceURIs []string, templateURIs []string) []string
}
