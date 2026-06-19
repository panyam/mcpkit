// Package common provides shared setup for auth examples.
// Each example is a persistent MCP server with different auth patterns.
// This package provides the in-process authorization server and common
// tool registrations so examples only contain auth-specific code.
package common

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/apiauth"
	"github.com/panyam/oneauth/testutil"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// ValidatorOption customizes the JWTConfig assembled by
// Env.NewValidator. Used to plumb tracing providers through without
// churning the 6 call sites that just want a default validator.
type ValidatorOption func(*auth.JWTConfig)

// WithMCPTracerProvider opts the validator into SEP-414 instrumentation
// of its own work (auth.jwks_lookup sub-span + mcp.auth.* attributes
// on the active dispatch span). Pair with the same TracerProvider you
// pass to server.WithTracerProvider so the spans share a pipeline.
func WithMCPTracerProvider(tp core.TracerProvider) ValidatorOption {
	return func(c *auth.JWTConfig) { c.TracerProvider = tp }
}

// WithOneauthTracerProvider opts oneauth's internal JWKS work into
// emitting its own spans (oneauth.jwks.refresh / key_lookup /
// signature_verify / ...). Typically the result of
// commonotel.UnderlyingOTelTP(mcpTP) so oneauth shares the same OTel
// pipeline as mcpkit.
func WithOneauthTracerProvider(tp oteltrace.TracerProvider) ValidatorOption {
	return func(c *auth.JWTConfig) { c.OneauthTracerProvider = tp }
}

// UpstreamIdpIssuer is the iss claim value the fixture's AS will accept
// on RFC 7523 §2.1 jwt-bearer assertions and RFC 8693 token-exchange
// subject_tokens. Stable per-process so MintUpstreamAssertion produces
// JWTs the AS validates.
const UpstreamIdpIssuer = "https://test-upstream-idp.example.invalid"

// Env holds the shared auth infrastructure for an example.
type Env struct {
	AS        *testutil.TestAuthServer
	Validator *auth.JWTValidator
	Scopes    []string

	// audience is the `aud` claim value examples want minted into
	// their tokens. It is also what the AS validator + issuer read on
	// every mint / validate via the AudienceFunc closure handed to
	// testutil. Set once by NewValidator before any tokens are minted
	// — the call sites are synchronous so plain field access is safe.
	audience string

	// upstreamKey is the RSA private key for the synthetic
	// UpstreamIdpIssuer the fixture trusts. Used by MintUpstreamAssertion
	// to sign assertions for the conformance suite's RFC 7523 / RFC 8693
	// flow-layer checks. Process-local — never persisted.
	upstreamKey *rsa.PrivateKey
}

// NewEnv creates an in-process authorization server with JWKS + token endpoint.
// Call NewValidator(audience, ...) after the MCP server starts to bind
// tokens to the server URL — the AS reads the audience on every mint
// + validate via the AudienceFunc closure plumbed below.
//
// The fixture advertises RFC 9207 (iss parameter) and the RFC 7523 §2.1
// jwt-bearer + RFC 8693 token-exchange grants by default — this is what
// the panyam/mcpconformance MCP-auth conformance suite Phase 3b/3c
// metadata-layer checks against. A throwaway "test-upstream-idp" key is
// generated per-process so the trusted-issuer registry is non-empty
// (which both wires the handler and auto-advertises the grants); no
// JWTs are minted for that issuer here — this is the metadata-layer
// turn-on, not the flow-layer (which awaits the conformance suite's
// OAuth flow-driver).
func NewEnv(scopes []string) *Env {
	upstreamKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}

	env := &Env{Scopes: scopes, upstreamKey: upstreamKey}
	as, err := testutil.NewAuthServer(
		testutil.WithScopes(scopes),
		testutil.WithIssParameterSupported(true),
		testutil.WithAudienceFunc(env.getAudience),
		testutil.WithTrustedAssertionIssuers([]apiauth.TrustedAssertionIssuer{{
			Issuer:             UpstreamIdpIssuer,
			PublicKey:          &upstreamKey.PublicKey,
			AcceptedAlgorithms: []string{"RS256"},
		}}),
	)
	if err != nil {
		log.Fatal(err)
	}
	env.AS = as
	return env
}

// getAudience is the closure handed to testutil.WithAudienceFunc. The
// AS validator + issuer call it on every mint and every validation.
// Returns "" until NewValidator binds the audience.
func (e *Env) getAudience() string {
	return e.audience
}

// NewValidator creates a JWTValidator pointed at the AS's JWKS.
// Call after the MCP server URL is known so audience can be set.
// Optional ValidatorOption args plumb tracing providers through —
// see WithMCPTracerProvider / WithOneauthTracerProvider.
func (e *Env) NewValidator(audience string, opts ...ValidatorOption) *auth.JWTValidator {
	e.audience = audience
	cfg := auth.JWTConfig{
		JWKSURL:   e.AS.JWKSURL(),
		Issuer:    e.AS.Issuer(),
		Audience:  audience,
		AllScopes: e.Scopes,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	v := auth.NewJWTValidator(cfg)
	v.Start()
	e.Validator = v
	return v
}

// MintUpstreamAssertion signs a JWT with the synthetic upstream IdP's
// private key (UpstreamIdpIssuer), suitable for use as an
// `assertion` parameter on a jwt-bearer (RFC 7523 §2.1) grant or as a
// `subject_token` (subject_token_type=jwt) on a token-exchange
// (RFC 8693) grant at the AS's token endpoint.
//
// The audience is the AS's own issuer identifier. RFC 7523 §3 requires
// the assertion's `aud` to identify the AS that will accept it. The
// jwt-bearer / token-exchange granter validates `aud` against its
// configured default audience and, when the trusted-issuer entry pins
// no Audiences (our setup), falls back to the AS issuer URL. The MCP
// resource URL bound via the AudienceFunc is the audience for access
// tokens, not for this upstream assertion, so we target the AS issuer
// here explicitly.
//
// iat / exp are set to a 5-min validity window. nbf is unset (the jwt
// library treats absent nbf as "no not-before constraint", which
// avoids clock-skew flakes in CI).
//
// Used by the panyam/mcpconformance suite's flow-layer Phase 3c check
// (auth-enterprise-managed-token-exchange-flow-shape) and is exposed
// via /demo/bootstrap as `tok_upstream_assertion` so the conformance
// test runner can read it without re-implementing the signing logic.
func (e *Env) MintUpstreamAssertion(subject string) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": UpstreamIdpIssuer,
		"sub": subject,
		"aud": e.AS.Issuer(),
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(e.upstreamKey)
	if err != nil {
		log.Fatal(err)
	}
	return signed
}

// MintToken creates a valid RS256 JWT for the given subject and scopes,
// with the correct audience for the MCP server.
func (e *Env) MintToken(subject string, scopes []string) string {
	claims := jwt.MapClaims{
		"sub": subject,
	}
	if e.audience != "" {
		claims["aud"] = e.audience
	}
	if len(scopes) > 0 {
		claims["scope"] = strings.Join(scopes, " ")
	}
	tok, err := e.AS.MintTokenWithClaims(claims)
	if err != nil {
		log.Fatal(err)
	}
	return tok
}

// MintExpiredToken creates a properly signed RS256 JWT whose `exp` claim is
// 1 hour in the past. Conformance fixtures use this to verify that servers
// reject expired tokens — the signature still verifies (same AS key), but
// the standard JWT exp validation MUST reject it.
func (e *Env) MintExpiredToken(subject string, scopes []string) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": subject,
		"iat": now.Add(-2 * time.Hour).Unix(),
		"exp": now.Add(-1 * time.Hour).Unix(),
	}
	if e.audience != "" {
		claims["aud"] = e.audience
	}
	if len(scopes) > 0 {
		claims["scope"] = strings.Join(scopes, " ")
	}
	tok, err := e.AS.MintTokenWithClaims(claims)
	if err != nil {
		log.Fatal(err)
	}
	return tok
}

// MintWrongAudienceToken creates a properly signed token whose `aud` claim
// points at a different resource. Conformance fixtures use this to verify
// audience enforcement — RFC 7519 requires servers to reject tokens whose
// aud doesn't match.
func (e *Env) MintWrongAudienceToken(subject string, scopes []string) string {
	claims := jwt.MapClaims{
		"sub": subject,
		"aud": "https://wrong-audience.example.invalid",
	}
	if len(scopes) > 0 {
		claims["scope"] = strings.Join(scopes, " ")
	}
	tok, err := e.AS.MintTokenWithClaims(claims)
	if err != nil {
		log.Fatal(err)
	}
	return tok
}

// MintWrongIssuerToken creates a token signed by THIS AS (so the signature
// verifies against the JWKS at the configured issuer) but whose `iss` claim
// claims a DIFFERENT issuer. RFC 7519 + standard JWT validation requires
// servers to reject when iss doesn't match the configured Issuer.
func (e *Env) MintWrongIssuerToken(subject string, scopes []string) string {
	claims := jwt.MapClaims{
		"sub": subject,
		"iss": "https://wrong-issuer.example.invalid",
	}
	if e.audience != "" {
		claims["aud"] = e.audience
	}
	if len(scopes) > 0 {
		claims["scope"] = strings.Join(scopes, " ")
	}
	tok, err := e.AS.MintTokenWithClaims(claims)
	if err != nil {
		log.Fatal(err)
	}
	return tok
}

// Close stops the authorization server and validator.
func (e *Env) Close() {
	if e.Validator != nil {
		e.Validator.Stop()
	}
	e.AS.Close()
}

// RegisterEchoTools adds standard tools to the server for auth demos:
//   - echo: no scope required, reports claims
//   - write-tool: requires "write" scope
//   - admin-tool: requires "admin" scope
func RegisterEchoTools(srv *server.Server) {
	srv.Register(core.TextTool[echoInput]("echo", "Echoes input and reports authenticated identity (no scope required)",
		func(ctx core.ToolContext, input echoInput) (string, error) {
			claims := ctx.AuthClaims()
			if claims != nil {
				return fmt.Sprintf("echo: %s (user: %s, scopes: %v)", input.Message, claims.Subject, claims.Scopes), nil
			}
			return fmt.Sprintf("echo: %s (anonymous)", input.Message), nil
		},
	))

	// Scope enforcement is declarative — auth.NewToolScopeMiddleware will
	// short-circuit unauthorized requests with HTTP 403 + WWW-Authenticate
	// before the handler runs. Servers that don't register the scope
	// middleware get RequiredScopes as inert metadata.
	srv.Register(core.TextTool[struct{}]("write-tool", "Requires 'write' scope",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "write ok", nil
		},
		core.WithToolRequiredScopes("write"),
	))

	srv.Register(core.TextTool[struct{}]("admin-tool", "Requires 'admin' scope",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "admin ok", nil
		},
		core.WithToolRequiredScopes("admin"),
	))
}

type echoInput struct {
	Message string `json:"message,omitempty" jsonschema:"description=Message to echo,default=hello"`
}
