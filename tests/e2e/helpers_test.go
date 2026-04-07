package e2e_test

// HTTP-level helpers for e2e tests. These supplement oneauth/testutil's
// token helpers with MCP-specific request patterns.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/ext/auth"
)

// StreamableHTTPAccept is the Accept header value required by the MCP Streamable
// HTTP transport spec (2025-11-25). Clients MUST accept both types.
const StreamableHTTPAccept = "application/json, text/event-stream"

// RawPOST sends a raw HTTP POST to the given URL with an optional Bearer token
// and returns the response. The body is a JSON string. Does not follow redirects.
// Callers must close resp.Body.
func RawPOST(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("RawPOST: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", StreamableHTTPAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("RawPOST: %v", err)
	}
	return resp
}

// RawDELETE sends a raw HTTP DELETE to the given URL with an optional Bearer token
// and an optional Mcp-Session-Id header. Callers must close resp.Body.
func RawDELETE(t *testing.T, url, token, sessionID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		t.Fatalf("RawDELETE: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("RawDELETE: %v", err)
	}
	return resp
}

// RawGET sends a raw HTTP GET to the given URL with an optional Bearer token.
// Callers must close resp.Body.
func RawGET(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("RawGET: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("RawGET: %v", err)
	}
	return resp
}

// ReadBody reads and closes the response body, returning it as a string.
func ReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	return string(body)
}

// initializeJSON is the JSON-RPC initialize request body.
const initializeJSON = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"e2e-test","version":"0.1.0"}}}`

// toolCallJSON builds a JSON-RPC tools/call request body.
func toolCallJSON(id int, name string, args map[string]any) string {
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params":  params,
	})
	return string(raw)
}

// ConnectMCPClient creates an mcpkit Client connected to the MCP server with
// the given bearer token. Performs the initialize handshake.
func (e *TestEnv) ConnectMCPClient(t *testing.T, token string) *core.Client {
	t.Helper()
	// Default transport is Streamable HTTP — no option needed.
	client := client.NewClient(
		e.MCPServerURL+"/mcp",
		core.ClientInfo{Name: "e2e-test", Version: "0.1.0"},
		client.WithClientBearerToken(token),
	)
	if err := client.Connect(); err != nil {
		t.Fatalf("ConnectMCPClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// These are used to verify that the auth module is imported correctly.
// The _ imports ensure the test binary links against mcpkit/auth.
var _ = auth.RequireScope
var _ core.Claims
