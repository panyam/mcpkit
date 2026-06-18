// Package e2e_test contains end-to-end integration tests for mcpkit's auth system.
//
// These tests wire up a real oneauth authorization server (in-process via
// oneauth/testutil.TestAuthServer) alongside a real mcpkit MCP server with
// JWTValidator. Tokens are RS256 JWTs issued by oneauth and validated by
// mcpkit's JWTValidator via JWKS.
//
// No external dependencies — everything runs in-process. Use "go test ./..."
// from this directory, or "make test-e2e" from the project root.
package e2e_test

import (
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	server "github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/testutil"
)

// testScopes are the scopes supported by the test MCP server.
var testScopes = []string{"tools:read", "tools:call", "admin:write"}

// audienceHolder owns the late-bound audience value the AS's
// validator + issuer read on every mint / validate. The MCP server
// URL is only known after buildMCPServer runs, so the AS is built
// once with a closure (audienceHolder.get) and the value is filled
// in once the URL is allocated. This is the canonical pattern oneauth
// docs/MIGRATION.md "Late-binding the audience" describes; testutil's
// WithAudienceFunc option plumbs the closure to OneAuthConfig.
type audienceHolder struct {
	mu  sync.Mutex
	val string
}

func (a *audienceHolder) get() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.val
}

func (a *audienceHolder) set(v string) {
	a.mu.Lock()
	a.val = v
	a.mu.Unlock()
}

// TestEnv holds an in-process oneauth authorization server and an mcpkit MCP
// server wired together via JWKS. Create one per test via NewTestEnv.
type TestEnv struct {
	// audVar backs the AudienceFunc the AS reads on every mint /
	// validate. Populated after the MCP server URL is known.
	audVar *audienceHolder

	// AS is the oneauth test authorization server (JWKS, token endpoint, AS metadata).
	AS *testutil.TestAuthServer

	// MCPServerURL is the base URL of the mcpkit MCP server.
	MCPServerURL string

	// mcpCleanup stops the MCP server (called via t.Cleanup).
	mcpCleanup func()
}

// NewTestEnv creates a fully wired test environment:
//  1. Starts an in-process oneauth auth server via testutil.NewTestAuthServer
//  2. Starts an in-process mcpkit MCP server with JWTValidator pointed at the AS's JWKS
//  3. Starts background JWKS refresh on the validator
//
// All servers are cleaned up automatically via t.Cleanup.
func NewTestEnv(t *testing.T) *TestEnv {
	t.Helper()

	env := &TestEnv{audVar: &audienceHolder{}}

	// Step 1: Start auth server. The audience closure returns "" until
	// the MCP server URL is allocated; tokens minted before that point
	// (none in practice) would have no aud claim, which is correct.
	env.AS = testutil.NewTestAuthServer(t,
		testutil.WithScopes(testScopes),
		testutil.WithAudienceFunc(env.audVar.get),
	)

	// Step 2: Start MCP server (uses AS's JWKS URL for JWT validation)
	env.buildMCPServer(t)

	// Step 3: Bind the audience to the MCP server URL. The AS issuer +
	// validator pick this up on every subsequent mint / validate.
	env.audVar.set(env.MCPServerURL)

	return env
}

// NewTestEnvWithPublicMethods creates a test environment like NewTestEnv but
// with WithPublicMethods configured on the MCP server.
func NewTestEnvWithPublicMethods(t *testing.T, methods ...string) *TestEnv {
	t.Helper()
	env := &TestEnv{audVar: &audienceHolder{}}
	env.AS = testutil.NewTestAuthServer(t,
		testutil.WithScopes(testScopes),
		testutil.WithAudienceFunc(env.audVar.get),
	)
	env.buildMCPServerWithOpts(t, server.WithPublicMethods(methods...))
	env.audVar.set(env.MCPServerURL)
	return env
}

// MintToken creates a valid RS256 JWT for the given user and scopes, with
// iss = auth server URL and aud = MCP server URL.
func (e *TestEnv) MintToken(t *testing.T, userID string, scopes []string) string {
	t.Helper()
	token, err := e.AS.MintTokenWithClaims(jwt.MapClaims{
		"sub":    userID,
		"aud":    e.MCPServerURL,
		"scopes": scopes,
	})
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	return token
}

// MintExpiredToken creates an RS256 JWT that expired 1 hour ago.
func (e *TestEnv) MintExpiredToken(t *testing.T, userID string) string {
	t.Helper()
	token, err := e.AS.MintTokenWithClaims(jwt.MapClaims{
		"sub":    userID,
		"aud":    e.MCPServerURL,
		"scopes": []string{"tools:read"},
		"exp":    time.Now().Add(-1 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("MintExpiredToken: %v", err)
	}
	return token
}

// MintTokenWithIssuer creates an RS256 JWT with a custom issuer (for wrong-issuer tests).
func (e *TestEnv) MintTokenWithIssuer(t *testing.T, userID, issuer string) string {
	t.Helper()
	token, err := e.AS.MintTokenWithClaims(jwt.MapClaims{
		"sub":    userID,
		"iss":    issuer,
		"aud":    e.MCPServerURL,
		"scopes": []string{"tools:read"},
	})
	if err != nil {
		t.Fatalf("MintTokenWithIssuer: %v", err)
	}
	return token
}

// MintTokenWithAudience creates an RS256 JWT with a custom audience (for wrong-audience tests).
func (e *TestEnv) MintTokenWithAudience(t *testing.T, userID, audience string) string {
	t.Helper()
	token, err := e.AS.MintTokenWithClaims(jwt.MapClaims{
		"sub":    userID,
		"aud":    audience,
		"scopes": []string{"tools:read"},
	})
	if err != nil {
		t.Fatalf("MintTokenWithAudience: %v", err)
	}
	return token
}
