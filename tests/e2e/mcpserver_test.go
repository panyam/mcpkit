package e2e_test

// MCP server wiring for e2e tests. Creates an mcpkit MCP server with
// JWTValidator, PRM endpoints, and test tools with varying scope requirements.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	server "github.com/panyam/mcpkit/server"
)

// buildMCPServer creates and starts an mcpkit MCP server with:
//   - JWTValidator pointed at the auth server's JWKS endpoint
//   - Protected Resource Metadata (PRM) at /.well-known/oauth-protected-resource
//   - Three test tools: echo (public), scoped-tool (requires tools:call), admin-tool (requires admin:write)
//   - Streamable HTTP transport at /mcp
//
// Uses a delegating handler pattern to resolve the chicken-and-egg URL problem:
// the MCP server URL is needed as the JWT audience, but isn't known until
// httptest.NewServer starts.
func (e *TestEnv) buildMCPServer(t *testing.T) {
	t.Helper()

	// Delegating handler — lets us wire the real mux after we know the server URL
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler == nil {
			http.Error(w, "server not ready", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	e.MCPServerURL = ts.URL

	// JWTValidator configured with the auth server's JWKS and the MCP server's URL as audience
	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             e.AS.JWKSURL(),
		Issuer:              e.AS.Issuer(),
		Audience:            ts.URL,
		ResourceMetadataURL: ts.URL + "/.well-known/oauth-protected-resource/mcp",
		AllScopes:           testScopes,
	})
	validator.Start()
	t.Cleanup(validator.Stop)

	// MCP server with auth
	srv := server.NewServer(
		core.ServerInfo{Name: "mcp-e2e-test", Version: "0.1.0"},
		server.WithAuth(validator),
		server.WithExtension(auth.AuthExtension{}),
	)

	// Public tool — no scope required, echoes the input and reports claims
	srv.RegisterTool(
		core.ToolDef{
			Name:        "echo",
			Description: "Echoes input and reports authenticated claims. No scope required.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			claims := core.AuthClaims(ctx)
			var args map[string]any
			json.Unmarshal(req.Arguments, &args)
			result := map[string]any{"msg": args["msg"]}
			if claims != nil {
				result["sub"] = claims.Subject
				result["iss"] = claims.Issuer
				result["aud"] = claims.Audience
				result["scopes"] = claims.Scopes
			}
			raw, _ := json.Marshal(result)
			return core.TextResult(string(raw)), nil
		},
	)

	// Scoped tool — requires "tools:call" scope
	srv.RegisterTool(
		core.ToolDef{
			Name:        "scoped-tool",
			Description: "Requires tools:call scope. Returns 'ok' on success.",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			if err := auth.RequireScope(ctx, "tools:call"); err != nil {
				return core.TextResult("error: " + err.Error()), nil
			}
			return core.TextResult("ok"), nil
		},
	)

	// Admin tool — requires "admin:write" scope
	srv.RegisterTool(
		core.ToolDef{
			Name:        "admin-tool",
			Description: "Requires admin:write scope. Returns 'admin ok' on success.",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			if err := auth.RequireScope(ctx, "admin:write"); err != nil {
				return core.TextResult("error: " + err.Error()), nil
			}
			return core.TextResult("admin ok"), nil
		},
	)

	// Wire the HTTP mux
	mux := http.NewServeMux()

	// Both Streamable HTTP and SSE transports enabled.
	// Handler() returns a mux with: Streamable at /mcp, SSE at /mcp/sse + /mcp/message.
	// Use prefix "/" so the inner mux handles all routing.
	mcpHandler := srv.Handler(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
	)
	mux.Handle("/mcp/", mcpHandler) // catch /mcp/sse, /mcp/message
	mux.Handle("/mcp", mcpHandler)  // catch /mcp (Streamable HTTP POST)

	// Protected Resource Metadata (RFC 9728) endpoints
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          ts.URL,
		AuthorizationServers: []string{e.AS.URL()},
		ScopesSupported:      testScopes,
		MCPPath:              "/mcp",
	})

	// Activate the delegating handler
	handler = mux
}
