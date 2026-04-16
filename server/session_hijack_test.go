package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAuthValidator is a minimal AuthValidator + ClaimsProvider for testing
// session hijacking. It extracts the Bearer token as the Subject claim.
type testAuthValidator struct{}

func (testAuthValidator) Validate(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		return &core.AuthError{Code: 401, Message: "missing bearer token"}
	}
	return nil
}

func (testAuthValidator) Claims(r *http.Request) *core.Claims {
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	return &core.Claims{Subject: token} // token IS the subject for testing
}

// initAuthSession sends an initialize request and returns the session ID.
func initAuthSession(t *testing.T, url, token string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp.Header.Get("Mcp-Session-Id")
}

// postToSession sends a tools/list request to an existing session.
func postToSession(t *testing.T, url, token, sessionID string) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// deleteSession sends a DELETE request to terminate a session.
func deleteSession(t *testing.T, url, token, sessionID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("DELETE", url+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func newAuthServer() (*server.Server, *httptest.Server) {
	srv := server.NewServer(
		core.ServerInfo{Name: "hijack-test", Version: "1.0"},
		server.WithAuth(testAuthValidator{}),
	)
	srv.Register(core.TextTool[struct{}]("ping", "Returns pong",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "pong", nil
		},
	))
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	return srv, ts
}

// TestStreamableHTTP_SessionHijackRejected verifies that a different user
// cannot use another user's session ID to make requests. User A creates
// a session, then user B tries to POST to it → 403 Forbidden.
func TestStreamableHTTP_SessionHijackRejected(t *testing.T) {
	_, ts := newAuthServer()
	defer ts.Close()

	// User A creates a session.
	sessionID := initAuthSession(t, ts.URL, "user-alice")
	require.NotEmpty(t, sessionID)

	// User A can use the session normally.
	resp := postToSession(t, ts.URL, "user-alice", sessionID)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "same user should be allowed")

	// User B tries to hijack Alice's session → 403.
	resp = postToSession(t, ts.URL, "user-bob", sessionID)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"different user should be rejected, got: %s", string(body))
}

// TestStreamableHTTP_SameUserAllowed verifies that the same user can make
// multiple requests on their own session without being rejected.
func TestStreamableHTTP_SameUserAllowed(t *testing.T) {
	_, ts := newAuthServer()
	defer ts.Close()

	sessionID := initAuthSession(t, ts.URL, "user-alice")

	for i := 0; i < 3; i++ {
		resp := postToSession(t, ts.URL, "user-alice", sessionID)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "same user request %d should succeed", i)
	}
}

// TestStreamableHTTP_NoAuthNoBinding verifies that sessions created without
// auth (no claims) allow any subsequent request — backward compatible with
// unauthenticated servers.
func TestStreamableHTTP_NoAuthNoBinding(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "noauth-test", Version: "1.0"})
	srv.Register(core.TextTool[struct{}]("ping", "Pong",
		func(ctx core.ToolContext, _ struct{}) (string, error) { return "pong", nil },
	))
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	defer ts.Close()

	// Create session without auth.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	sessionID := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()
	require.NotEmpty(t, sessionID)

	// Any request should work (no principal binding).
	req2, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	req2.Header.Set("Mcp-Session-Id", sessionID)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}

// TestStreamableHTTP_DeleteHijackRejected verifies that a different user
// cannot DELETE another user's session.
func TestStreamableHTTP_DeleteHijackRejected(t *testing.T) {
	_, ts := newAuthServer()
	defer ts.Close()

	sessionID := initAuthSession(t, ts.URL, "user-alice")

	// User B tries to delete Alice's session → 403.
	resp := deleteSession(t, ts.URL, "user-bob", sessionID)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"different user should not be able to delete session")

	// Alice can still use her session (it wasn't deleted).
	resp = postToSession(t, ts.URL, "user-alice", sessionID)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"session should still be alive after failed hijack delete")

	// Alice can delete her own session.
	resp = deleteSession(t, ts.URL, "user-alice", sessionID)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"same user should be able to delete their session")
}

// Verify testAuthValidator implements ClaimsProvider.
var _ core.ClaimsProvider = testAuthValidator{}

// Ensure proper JSON response format for initialize.
func init() {
	// Register empty result type for json.Unmarshal in tests.
	_ = json.RawMessage(`{}`)
}
