package keycloak_test

// Test utilities for mcpkit Keycloak interop tests. Provides:
//   - Keycloak connectivity detection (skip if not running)
//   - Realm URL construction
//   - Thin wrappers around oneauth/testutil shared helpers
//   - MCP server wiring with JWTValidator pointed at Keycloak's JWKS
//
// Keycloak URL defaults to http://localhost:8180. Override with KEYCLOAK_URL env var.
// Tests skip gracefully when Keycloak is not reachable — run "make upkcl" to start.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/oneauth/testutil"
)

const (
	defaultKeycloakURL = "http://localhost:8180"
	realmName          = "mcpkit-test"

	// Clients defined in realm.json
	confidentialClientID     = "mcp-confidential"
	confidentialClientSecret = "mcp-test-secret-for-confidential"

	// Test user defined in realm.json
	testUsername = "mcp-testuser"
	testPassword = "testpassword"

	// MCP scopes (defined as client scopes in realm.json)
	scopeToolsRead  = "tools-read"
	scopeToolsCall  = "tools-call"
	scopeAdminWrite = "admin-write"
)

// keycloakURL returns the Keycloak base URL from env or default.
func keycloakURL() string {
	if u := os.Getenv("KEYCLOAK_URL"); u != "" {
		return u
	}
	return defaultKeycloakURL
}

// realmURL returns the full realm URL (e.g., http://localhost:8180/realms/mcpkit-test).
func realmURL() string {
	return keycloakURL() + "/realms/" + realmName
}

// skipIfKeycloakNotRunning checks if Keycloak's mcpkit-test realm is reachable.
// Skips the test if not — allows running "go test ./..." without Docker.
func skipIfKeycloakNotRunning(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(realmURL())
	if err != nil {
		t.Skipf("Keycloak not reachable at %s: %v (run 'make upkcl' to start)", keycloakURL(), err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("Keycloak realm %s not ready (status %d)", realmName, resp.StatusCode)
	}
}

// discoverOIDC fetches the OIDC discovery document from the Keycloak realm.
func discoverOIDC(t *testing.T) testutil.OIDCConfig {
	t.Helper()
	return testutil.DiscoverOIDC(t, realmURL())
}

// getClientCredentialsToken acquires a token via client_credentials grant.
func getClientCredentialsToken(t *testing.T, tokenEndpoint string, scopes ...string) testutil.TokenResponse {
	t.Helper()
	return testutil.GetClientCredentialsToken(t, tokenEndpoint, confidentialClientID, confidentialClientSecret, scopes...)
}

// getPasswordToken acquires a token via password grant for the test user.
func getPasswordToken(t *testing.T, tokenEndpoint string) testutil.TokenResponse {
	t.Helper()
	return testutil.GetPasswordToken(t, tokenEndpoint, confidentialClientID, confidentialClientSecret, testUsername, testPassword)
}

// MCPTestEnv holds an in-process mcpkit MCP server with JWTValidator configured
// to validate Keycloak-issued tokens via JWKS. The MCP server runs as httptest.Server.
type MCPTestEnv struct {
	MCPServer *httptest.Server
	OIDC      testutil.OIDCConfig
}

// NewMCPTestEnv creates an mcpkit MCP server with JWTValidator pointed at
// Keycloak's JWKS endpoint. The server registers test tools (echo, scoped-tool,
// admin-tool) with varying scope requirements.
//
// Requires Keycloak to be running (caller should call skipIfKeycloakNotRunning first).
func NewMCPTestEnv(t *testing.T) *MCPTestEnv {
	t.Helper()

	cfg := discoverOIDC(t)

	// Delegating handler for chicken-and-egg URL resolution
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler == nil {
			http.Error(w, "server not ready", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	// JWTValidator pointed at Keycloak's JWKS
	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             cfg.JWKSURI,
		Issuer:              cfg.Issuer,
		Audience:            "", // Keycloak doesn't set aud by default for client_credentials
		ResourceMetadataURL: ts.URL + "/.well-known/oauth-protected-resource/mcp",
		AllScopes:           []string{scopeToolsRead, scopeToolsCall, scopeAdminWrite},
	})
	validator.Start()
	t.Cleanup(validator.Stop)

	srv := server.NewServer(
		core.ServerInfo{Name: "mcp-keycloak-test", Version: "0.1.0"},
		server.WithAuth(validator),
	)

	// Echo tool — returns claims info, no scope required
	srv.RegisterTool(
		core.ToolDef{
			Name:        "echo",
			Description: "Echoes input and reports claims. No scope required.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			claims := core.AuthClaims(ctx)
			var args map[string]any
			json.Unmarshal(req.Arguments, &args)
			result := map[string]any{"msg": args["msg"]}
			if claims != nil {
				result["sub"] = claims.Subject
				result["scopes"] = claims.Scopes
			}
			raw, _ := json.Marshal(result)
			return core.TextResult(string(raw)), nil
		},
	)

	// Scoped tool — requires tools-call scope
	srv.RegisterTool(
		core.ToolDef{
			Name:        "scoped-tool",
			Description: "Requires tools-call scope.",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			if err := auth.RequireScope(ctx, scopeToolsCall); err != nil {
				return core.TextResult("error: " + err.Error()), nil
			}
			return core.TextResult("ok"), nil
		},
	)

	// Wire mux
	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          ts.URL,
		AuthorizationServers: []string{cfg.Issuer},
		ScopesSupported:      []string{scopeToolsRead, scopeToolsCall, scopeAdminWrite},
		MCPPath:              "/mcp",
	})
	handler = mux

	return &MCPTestEnv{
		MCPServer: ts,
		OIDC:      cfg,
	}
}
