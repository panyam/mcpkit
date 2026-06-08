package auth

import (
	"net/http"
	"strings"
	"time"

	conc "github.com/panyam/gocurrent"
	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/oneauth/apiauth"
)

// IntrospectionValidator validates MCP requests using bearer tokens
// against an RFC 7662 OAuth 2.0 Token Introspection endpoint. It
// implements mcpcore.AuthValidator and mcpcore.ClaimsProvider by
// wrapping oneauth's IntrospectionValidator (which owns the wire
// format and the introspection-response cache) and mapping the result
// into mcpkit's core.Claims shape.
//
// Use IntrospectionValidator instead of JWTValidator when:
//
//   - Token revocation must be visible synchronously (introspection's
//     cache TTL bounds revocation lag; JWT/JWKS validation can't see
//     revocation until the token expires).
//   - The resource server cannot reach the JWKS endpoint but can reach
//     a server-to-server introspection endpoint authenticated with
//     client credentials.
//   - The authorization server issues opaque (non-JWT) tokens.
//
// Tenant encoding: by default the Subject on the returned core.Claims
// is "<realm>/<sub>" where <realm> is parsed from the introspection
// response's iss claim (Keycloak's .../realms/<realm> URL shape) and
// <sub> is the response's sub claim. Hook authors split these halves
// via auth.TenantOf / auth.SubjectOf. The default is overridable per
// IntrospectionConfig.TenantMapper.
//
// Usage:
//
//	validator := auth.NewIntrospectionValidator(auth.IntrospectionConfig{
//	    IntrospectionURL: "https://auth.example.com/oauth/introspect",
//	    ClientID:         "mcp-resource-server",
//	    ClientSecret:     os.Getenv("OAUTH_CLIENT_SECRET"),
//	    CacheTTL:         30 * time.Second,
//	})
//	srv := mcp.NewServer(info, mcp.WithAuth(validator))
type IntrospectionValidator struct {
	v   *apiauth.IntrospectionValidator
	cfg IntrospectionConfig

	// recentClaims caches the most recently validated claims by raw
	// token string so Claims(r) can return them without a second
	// introspection round trip. Mirrors JWTValidator.recentClaims —
	// the entry is popped by Claims() (LoadAndDelete), so this is
	// effectively a one-shot per-request handoff. Persistent caching
	// is the introspection wire layer's job (cfg.CacheTTL), not
	// mcpkit's.
	recentClaims conc.SyncMap[string, *mcpcore.Claims]
}

// TenantMapper extracts (tenant, subject) from an introspection
// response. The validator builds Claims.Subject as tenant +
// PrincipalTenantSeparator + subject when tenant is non-empty,
// otherwise stamps Claims.Subject as just subject — single-tenant
// deployments pass tenant="" and round-trip the subject directly.
//
// Mappers MUST NOT return subject == "" — Validate rejects the request
// with 401 when no subject is recoverable, since events.WebhookStore
// keys on principal and an empty principal is meaningless.
//
// The default mapper (realm-from-issuer) is sufficient for every
// Keycloak deployment whose realm == tenant; override only when a
// deployment maps tenant from a custom claim (e.g., an admin-mapped
// "organization" attribute). Until a concrete consumer needs the
// override, callers should leave IntrospectionConfig.TenantMapper nil
// — the default is documented and stable.
type TenantMapper func(*apiauth.IntrospectionResult) (tenant, subject string)

// IntrospectionConfig configures an IntrospectionValidator. Construction
// goes through NewIntrospectionValidator — the zero value is not safe
// (no endpoint, no credentials).
type IntrospectionConfig struct {
	// IntrospectionURL is the OAuth 2.0 Token Introspection endpoint
	// (RFC 7662). Required.
	IntrospectionURL string

	// ClientID and ClientSecret authenticate this resource server to
	// the introspection endpoint via HTTP Basic auth
	// (client_secret_basic). Both required.
	ClientID     string
	ClientSecret string

	// HTTPClient overrides the HTTP client used for introspection
	// requests. Nil means http.DefaultClient. Set this when a deployment
	// needs custom timeouts, mTLS, or to point at a sidecar proxy.
	HTTPClient *http.Client

	// CacheTTL bounds the staleness window for introspection responses.
	// A revoked token MAY remain "active" in the cache for up to
	// CacheTTL after the authorization server actually revokes it —
	// this is the load-bearing knob for the prod-events walkthrough's
	// "token revocation as auth-visibility step" demo. Default 0 (no
	// caching, every request hits the AS). Recommended 30s.
	CacheTTL time.Duration

	// ResourceMetadataURL is included in WWW-Authenticate headers on
	// 401 responses (RFC 9728). Same field as on JWTConfig.
	ResourceMetadataURL string

	// RequiredScopes are checked on every request (global minimum).
	// 403 + WWW-Authenticate scope hint on missing scopes.
	RequiredScopes []string

	// AllScopes is the complete set of scopes this server supports.
	// Included in WWW-Authenticate 401 headers for client scope
	// selection — same shape as JWTConfig.AllScopes.
	AllScopes []string

	// TenantMapper extracts (tenant, subject) from the introspection
	// response. nil means the default realm-from-issuer-URL mapper —
	// the right choice for any Keycloak deployment whose realm =
	// tenant. Override when a deployment maps tenant from a custom
	// claim, but note that oneauth's IntrospectionResult today carries
	// only RFC 7662 well-known fields; custom-claim mapping requires
	// extending oneauth (tracked as a future enhancement).
	TenantMapper TenantMapper
}

// NewIntrospectionValidator constructs a validator backed by oneauth's
// IntrospectionValidator. Required fields (IntrospectionURL, ClientID,
// ClientSecret) MUST be set on cfg; this constructor does not panic on
// missing fields — the resulting validator will reject every request
// with a 401 because the underlying introspection call fails. Callers
// SHOULD validate cfg before construction.
func NewIntrospectionValidator(cfg IntrospectionConfig) *IntrospectionValidator {
	if cfg.TenantMapper == nil {
		cfg.TenantMapper = defaultTenantMapper
	}
	return &IntrospectionValidator{
		v: &apiauth.IntrospectionValidator{
			IntrospectionURL: cfg.IntrospectionURL,
			ClientID:         cfg.ClientID,
			ClientSecret:     cfg.ClientSecret,
			HTTPClient:       cfg.HTTPClient,
			CacheTTL:         cfg.CacheTTL,
		},
		cfg: cfg,
	}
}

// Validate implements mcpcore.AuthValidator. Extracts the bearer
// token, calls the introspection endpoint via oneauth (honors the
// configured CacheTTL), maps the response into core.Claims using
// IntrospectionConfig.TenantMapper, and checks RequiredScopes. Stashes
// the claims for Claims(r) to retrieve on the same request.
//
// Error semantics mirror JWTValidator: 401 + WWW-Authenticate for
// every authentication failure (missing header, inactive token,
// network error, missing subject); 403 + scope-hint for insufficient
// scope.
func (v *IntrospectionValidator) Validate(r *http.Request) error {
	const prefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, prefix) {
		return v.unauthorized("missing or invalid Authorization header")
	}
	token := authHeader[len(prefix):]

	result, err := v.v.Validate(token)
	if err != nil {
		return v.unauthorized("introspection failed: " + err.Error())
	}
	if !result.Active {
		return v.unauthorized("token is not active")
	}

	tenant, subject := v.cfg.TenantMapper(result)
	if subject == "" {
		return v.unauthorized("introspection response missing subject")
	}

	principal := subject
	if tenant != "" {
		principal = tenant + PrincipalTenantSeparator + subject
	}

	var scopes []string
	if result.Scope != "" {
		scopes = strings.Fields(result.Scope)
	}

	if len(v.cfg.RequiredScopes) > 0 && !containsAllScopes(scopes, v.cfg.RequiredScopes) {
		return &mcpcore.AuthError{
			Code:            http.StatusForbidden,
			Message:         "insufficient scope",
			WWWAuthenticate: WWWAuth403(v.cfg.RequiredScopes...),
		}
	}

	claims := &mcpcore.Claims{
		Subject: principal,
		Issuer:  result.Iss,
		Scopes:  scopes,
		Extra: map[string]any{
			"tenant":  tenant,
			"raw_sub": result.Sub,
		},
	}
	if result.Aud != nil {
		claims.Audience = audienceSlice(result.Aud)
	}
	if result.ClientID != "" {
		claims.Extra["client_id"] = result.ClientID
	}
	if result.Jti != "" {
		claims.Extra["jti"] = result.Jti
	}

	v.recentClaims.Store(token, claims)
	return nil
}

// Claims implements mcpcore.ClaimsProvider. Returns the claims that
// the most recent Validate(r) call stashed for the same bearer token,
// or nil if there is no such record. Like JWTValidator.Claims, this is
// a one-shot read: LoadAndDelete drops the entry so the next
// per-request lookup misses (the validator's recentClaims is not a
// persistent cache — that's the introspection layer's job).
func (v *IntrospectionValidator) Claims(r *http.Request) *mcpcore.Claims {
	const prefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, prefix) {
		return nil
	}
	token := authHeader[len(prefix):]
	if claims, ok := v.recentClaims.LoadAndDelete(token); ok {
		return claims
	}
	return nil
}

// unauthorized returns a 401 AuthError with a WWW-Authenticate header
// pointing at the configured ResourceMetadataURL and the full AllScopes
// list, matching the JWTValidator shape so clients see uniform PRM
// metadata regardless of which validator is in use.
func (v *IntrospectionValidator) unauthorized(msg string) *mcpcore.AuthError {
	return &mcpcore.AuthError{
		Code:            http.StatusUnauthorized,
		Message:         msg,
		WWWAuthenticate: WWWAuth401(v.cfg.ResourceMetadataURL, v.cfg.AllScopes...),
	}
}

// defaultTenantMapper extracts the realm name from the introspection
// response's iss claim and uses the response's sub as the subject.
// Matches the Keycloak issuer URL convention .../realms/<realm>; for
// non-Keycloak issuers (no /realms/ segment) the tenant collapses to
// "" — single-tenant deployments fall back to subject-only principals
// transparently.
func defaultTenantMapper(result *apiauth.IntrospectionResult) (tenant, subject string) {
	return realmFromIssuer(result.Iss), result.Sub
}

// realmFromIssuer parses the Keycloak realm name out of an issuer URL.
// Returns "" if the URL has no /realms/ segment (non-Keycloak AS).
// The realm name is everything after the LAST /realms/ segment, with
// any trailing path stripped — Keycloak emits issuers like
// http://localhost:8081/realms/tenant-a verbatim, no trailing path
// in normal config, so the slash-strip is purely defensive.
func realmFromIssuer(iss string) string {
	const marker = "/realms/"
	i := strings.LastIndex(iss, marker)
	if i < 0 {
		return ""
	}
	realm := iss[i+len(marker):]
	if j := strings.IndexByte(realm, '/'); j >= 0 {
		realm = realm[:j]
	}
	return realm
}

// audienceSlice normalizes the introspection response's aud field into
// a slice. The RFC 7662 audience claim is either a string or a string
// array; oneauth surfaces it as any to preserve both shapes.
func audienceSlice(aud any) []string {
	switch v := aud.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, a := range v {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	default:
		return nil
	}
}

// containsAllScopes returns true iff every required scope is present
// in have. Local copy of the helper rather than importing oneauth's —
// the function is one loop, and the local copy keeps the dep surface
// out of test runs.
func containsAllScopes(have, required []string) bool {
	for _, r := range required {
		found := false
		for _, h := range have {
			if h == r {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
