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
)

// Env holds the shared auth infrastructure for an example.
type Env struct {
	AS        *testutil.TestAuthServer
	Validator *auth.JWTValidator
	Scopes    []string
}

// NewEnv creates an in-process authorization server with JWKS + token endpoint.
// Call SetAudience(url) after the MCP server starts to bind tokens to the server URL.
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

	as, err := testutil.NewAuthServer(
		testutil.WithScopes(scopes),
		testutil.WithIssParameterSupported(true),
		testutil.WithTrustedAssertionIssuers([]apiauth.TrustedAssertionIssuer{{
			Issuer:             "https://test-upstream-idp.example.invalid",
			PublicKey:          &upstreamKey.PublicKey,
			AcceptedAlgorithms: []string{"RS256"},
		}}),
	)
	if err != nil {
		log.Fatal(err)
	}
	return &Env{AS: as, Scopes: scopes}
}

// NewValidator creates a JWTValidator pointed at the AS's JWKS.
// Call after the MCP server URL is known so audience can be set.
func (e *Env) NewValidator(audience string) *auth.JWTValidator {
	e.AS.APIAuth.JWTAudience = audience
	v := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:   e.AS.JWKSURL(),
		Issuer:    e.AS.Issuer(),
		Audience:  audience,
		AllScopes: e.Scopes,
	})
	v.Start()
	e.Validator = v
	return v
}

// MintToken creates a valid RS256 JWT for the given subject and scopes,
// with the correct audience for the MCP server.
func (e *Env) MintToken(subject string, scopes []string) string {
	claims := jwt.MapClaims{
		"sub": subject,
	}
	if e.AS.APIAuth.JWTAudience != "" {
		claims["aud"] = e.AS.APIAuth.JWTAudience
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
	if e.AS.APIAuth.JWTAudience != "" {
		claims["aud"] = e.AS.APIAuth.JWTAudience
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
	if e.AS.APIAuth.JWTAudience != "" {
		claims["aud"] = e.AS.APIAuth.JWTAudience
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
