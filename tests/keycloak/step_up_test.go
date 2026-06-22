package keycloak_test

// SEP-2350 step-up scope-challenge coverage. The existing
// TestKeycloak_MCPServer_ScopeDenied test exercises RequireScope (the
// handler-level helper that returns an error string), not the transport-
// level 403 + WWW-Authenticate flow SEP-2350 actually mandates. These
// tests cover the middleware-emitted challenge so the conformance suite's
// expectations and mcpkit's behavior stay in lockstep.
//
// Both wire modes (default Dual + SEP-2575 stateless) are exercised: the
// fix from panyam/mcpkit issue 815 ensures the typed *core.AuthError
// surfaces on the stateless wire too; this test locks that in.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	server "github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/require"
)

const (
	stepUpToolName = "admin_call"
)

// newStepUpEnv builds an mcpkit MCP server wired with
// auth.NewToolScopeMiddleware against the Keycloak realm. Distinct from
// NewMCPTestEnv: this one declares the scope requirement via
// core.ToolDef.RequiredScopes + the middleware so a scope failure produces
// HTTP 403 + WWW-Authenticate at the transport, instead of a handler-
// returned error string. extraOpts append to the base server.Options so
// callers can flip wire mode via server.WithStatelessMode.
func newStepUpEnv(t *testing.T, extraTransport ...server.TransportOption) (*httptest.Server, string) {
	t.Helper()

	cfg := discoverOIDC(t)

	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler == nil {
			http.Error(w, "server not ready", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	prmURL := ts.URL + "/.well-known/oauth-protected-resource/mcp"
	allScopes := []string{scopeToolsRead, scopeToolsCall, scopeAdminWrite}

	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:             cfg.JWKSURI,
		Issuer:              cfg.Issuer,
		Audience:            "",
		ResourceMetadataURL: prmURL,
		AllScopes:           allScopes,
	})
	validator.Start()
	t.Cleanup(validator.Stop)

	srv := server.NewServer(
		core.ServerInfo{Name: "mcp-keycloak-step-up", Version: "0.1.0"},
		server.WithAuth(validator),
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:           stepUpToolName,
			Description:    "Requires admin-write scope; middleware emits 403 before handler runs.",
			InputSchema:    json.RawMessage(`{"type":"object"}`),
			RequiredScopes: []string{scopeAdminWrite},
		},
		func(_ core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult(stepUpToolName + ": ok"), nil
		},
	)
	srv.UseMiddleware(auth.NewToolScopeMiddleware(srv.Registry(),
		auth.WithResourceMetadataURL(prmURL),
	))

	transportOpts := append([]server.TransportOption{server.WithStreamableHTTP(true)}, extraTransport...)
	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(transportOpts...))
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          ts.URL,
		AuthorizationServers: []string{cfg.Issuer},
		ScopesSupported:      allScopes,
		MCPPath:              "/mcp",
	})
	handler = mux

	return ts, cfg.TokenEndpoint
}

// toolsCallStateless sends a raw stateless tools/call POST and returns the
// HTTP status, WWW-Authenticate header value, and response body. Mirrors
// the wire shape the conformance suite's scope-challenge scenario uses.
func toolsCallStateless(t *testing.T, serverURL, bearer, toolName string) (int, string, string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"name":"` + toolName + `","arguments":{},` +
		`"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"step-up-test","version":"0.0.0"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}` +
		`}}}`

	req, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), string(raw)
}

// TestKeycloak_StepUp_StatelessWire exercises the SEP-2350 wire shape on
// the SEP-2575 stateless wire end-to-end against a real Keycloak realm.
// Closes the gap mcpkit issue 815 documented and that the conformance
// scenario in panyam/mcpconformance PR 19 originally surfaced.
func TestKeycloak_StepUp_StatelessWire(t *testing.T) {
	skipIfKeycloakNotRunning(t)

	ts, tokenEndpoint := newStepUpEnv(t, server.WithStatelessMode(stateless.ModeStateless))
	mcpURL := ts.URL + "/mcp"

	insufficient := getClientCredentialsToken(t, tokenEndpoint, scopeToolsRead)
	sufficient := getClientCredentialsToken(t, tokenEndpoint, scopeToolsRead, scopeToolsCall, scopeAdminWrite)

	status, wwwAuth, _ := toolsCallStateless(t, mcpURL, insufficient.AccessToken, stepUpToolName)
	require.Equal(t, http.StatusForbidden, status, "under-scoped token must produce 403")
	require.NotEmpty(t, wwwAuth, "403 must carry a WWW-Authenticate header")
	require.True(t, strings.HasPrefix(wwwAuth, "Bearer "), "challenge must use Bearer scheme; got %q", wwwAuth)
	require.Contains(t, wwwAuth, `error="insufficient_scope"`, "challenge must advertise insufficient_scope")
	require.Contains(t, wwwAuth, `scope="`+scopeAdminWrite+`"`, "challenge must advertise the required scope only (least-privilege); got %q", wwwAuth)
	require.NotContains(t, wwwAuth, scopeToolsRead, "challenge must NOT leak the caller's granted scopes")
	require.Contains(t, wwwAuth, `resource_metadata="`, "challenge must include the RFC 9728 PRM link")

	statusOK, _, body := toolsCallStateless(t, mcpURL, sufficient.AccessToken, stepUpToolName)
	require.Equal(t, http.StatusOK, statusOK, "properly-scoped token must succeed; body=%s", body)
	require.Contains(t, body, stepUpToolName+": ok", "handler should have run; body=%s", body)
}

// TestKeycloak_StepUp_LegacyWire exercises the same SEP-2350 wire shape on
// the default Dual / legacy session wire. The legacy path is the one that
// has shipped longest in mcpkit; this lock-in test ensures the PRM-link
// fix and middleware emission continue to work alongside the stateless
// path. Performs initialize handshake first to obtain a session id, then
// fires tools/call with the under-scoped token.
func TestKeycloak_StepUp_LegacyWire(t *testing.T) {
	skipIfKeycloakNotRunning(t)

	ts, tokenEndpoint := newStepUpEnv(t)
	mcpURL := ts.URL + "/mcp"

	sufficient := getClientCredentialsToken(t, tokenEndpoint, scopeToolsRead, scopeToolsCall, scopeAdminWrite)
	sessionID := legacyInitialize(t, mcpURL, sufficient.AccessToken)
	require.NotEmpty(t, sessionID, "initialize must return an Mcp-Session-Id")

	insufficient := getClientCredentialsToken(t, tokenEndpoint, scopeToolsRead)
	status, wwwAuth, _ := legacyToolsCall(t, mcpURL, sessionID, insufficient.AccessToken, stepUpToolName)
	require.Equal(t, http.StatusForbidden, status, "under-scoped token must produce 403 on legacy wire")
	require.Contains(t, wwwAuth, `error="insufficient_scope"`)
	require.Contains(t, wwwAuth, `scope="`+scopeAdminWrite+`"`)
	require.Contains(t, wwwAuth, `resource_metadata="`)

	statusOK, _, body := legacyToolsCall(t, mcpURL, sessionID, sufficient.AccessToken, stepUpToolName)
	require.Equal(t, http.StatusOK, statusOK, "properly-scoped token must succeed on legacy wire; body=%s", body)
	require.Contains(t, body, stepUpToolName+": ok")
}

// legacyInitialize runs the legacy initialize handshake plus the required
// notifications/initialized follow-up and returns the server-assigned
// Mcp-Session-Id. The legacy wire requires the session to be fully
// initialized before any tools/call dispatch; sending just `initialize`
// without the follow-up notification leaves the session in a half-open
// state and tools/call returns -32600 "server not initialized".
func legacyInitialize(t *testing.T, serverURL, bearer string) string {
	t.Helper()
	initBody := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{` +
		`"protocolVersion":"2025-11-25",` +
		`"capabilities":{},` +
		`"clientInfo":{"name":"step-up-test","version":"0.0.0"}` +
		`}}`
	req, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewBufferString(initBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "initialize must succeed")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "initialize must return an Mcp-Session-Id")

	notifBody := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	notifReq, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewBufferString(notifBody))
	require.NoError(t, err)
	notifReq.Header.Set("Content-Type", "application/json")
	notifReq.Header.Set("Accept", "application/json, text/event-stream")
	notifReq.Header.Set("Authorization", "Bearer "+bearer)
	notifReq.Header.Set("Mcp-Session-Id", sessionID)
	notifResp, err := http.DefaultClient.Do(notifReq)
	require.NoError(t, err)
	notifResp.Body.Close()
	require.Less(t, notifResp.StatusCode, 300, "notifications/initialized must succeed; got %d", notifResp.StatusCode)

	return sessionID
}

// legacyToolsCall sends a tools/call POST on the legacy wire with the
// session id from initialize. Returns status + WWW-Authenticate + body
// like the stateless variant for assertion symmetry.
func legacyToolsCall(t *testing.T, serverURL, sessionID, bearer, toolName string) (int, string, string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"name":"` + toolName + `","arguments":{}}}`
	req, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), string(raw)
}
