package client_test

// Unit tests for client-side 401/403 auth retry logic (doWithAuthRetry).
// These use mock HTTP servers and token sources to test retry behavior
// without real JWT infrastructure. For E2E tests with real JWTs, see
// tests/e2e/auth_retry_test.go.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	client "github.com/panyam/mcpkit/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTokenSource tracks Token() calls and returns configurable tokens.
type mockTokenSource struct {
	tokens []string // tokens returned on successive Token() calls
	calls  atomic.Int32
}

func (m *mockTokenSource) Token() (string, error) {
	idx := int(m.calls.Add(1)) - 1
	if idx < len(m.tokens) {
		return m.tokens[idx], nil
	}
	return m.tokens[len(m.tokens)-1], nil // repeat last
}

// mockScopeAwareTokenSource extends mockTokenSource with scope step-up.
type mockScopeAwareTokenSource struct {
	mockTokenSource
	scopesCalled [][]string // scopes passed to TokenForScopes
}

func (m *mockScopeAwareTokenSource) TokenForScopes(scopes []string) (string, error) {
	m.scopesCalled = append(m.scopesCalled, scopes)
	return m.Token()
}

// TestClient_401_RetryWithRefresh verifies that on 401, the transport calls
// Token() to refresh the token and retries the request. The mock server
// returns 401 on the first request and 200 on the second.
func TestClient_401_RetryWithRefresh(t *testing.T) {
	var requestCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "token expired", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`))
	}))
	defer ts.Close()

	tokenSrc := &mockTokenSource{tokens: []string{"old-token", "new-token"}}

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", ts.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	}

	resp, err := client.DoWithAuthRetry(tokenSrc, buildReq, http.DefaultClient.Do)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, int32(2), requestCount.Load(), "should have made 2 requests")
	// Token() called 3 times: setAuthHeader(req1) + refresh on 401 + setAuthHeader(req2)
	assert.Equal(t, int32(3), tokenSrc.calls.Load(), "Token() called 3 times")
}

// TestClient_401_StaticTokenGivesUp verifies that a static token source
// (which always returns the same token) results in a ClientAuthError after
// the retry fails with the same 401.
func TestClient_401_StaticTokenGivesUp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()

	tokenSrc := &localStaticToken{token: "static-token"}

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", ts.URL, strings.NewReader(`{}`))
	}

	_, err := client.DoWithAuthRetry(tokenSrc, buildReq, http.DefaultClient.Do)
	require.Error(t, err)

	var authErr *client.ClientAuthError
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, 401, authErr.StatusCode)
}

// TestClient_403_ScopeStepUp verifies that on 403 with a WWW-Authenticate
// header containing required scopes, the transport calls TokenForScopes
// on a ScopeAwareTokenSource and retries the request.
func TestClient_403_ScopeStepUp(t *testing.T) {
	var requestCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="admin:write"`)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`))
	}))
	defer ts.Close()

	tokenSrc := &mockScopeAwareTokenSource{
		mockTokenSource: mockTokenSource{tokens: []string{"narrow-token", "broad-token"}},
	}

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", ts.URL, strings.NewReader(`{}`))
	}

	resp, err := client.DoWithAuthRetry(tokenSrc, buildReq, http.DefaultClient.Do)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	require.Len(t, tokenSrc.scopesCalled, 1)
	assert.Equal(t, []string{"admin:write"}, tokenSrc.scopesCalled[0])
}

// TestClient_403_NoScopeAware verifies that 403 with a plain TokenSource
// (not ScopeAwareTokenSource) returns a ClientAuthError with status code 403
// and RequiredScopes parsed from the WWW-Authenticate header.
func TestClient_403_NoScopeAware(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="admin:write tools:call"`)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer ts.Close()

	tokenSrc := &mockTokenSource{tokens: []string{"narrow-token"}}

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", ts.URL, strings.NewReader(`{}`))
	}

	_, err := client.DoWithAuthRetry(tokenSrc, buildReq, http.DefaultClient.Do)
	require.Error(t, err)

	var authErr *client.ClientAuthError
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, 403, authErr.StatusCode)
	assert.Contains(t, authErr.RequiredScopes, "admin:write")
	assert.Contains(t, authErr.RequiredScopes, "tools:call")
}

// TestClient_RetryLimit_401Then403 verifies that the retry budget allows
// one 401 retry AND one 403 retry (total 2 retries, 3 requests).
func TestClient_RetryLimit_401Then403(t *testing.T) {
	var requestCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		switch n {
		case 1:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case 2:
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="admin:write"`)
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`))
		}
	}))
	defer ts.Close()

	tokenSrc := &mockScopeAwareTokenSource{
		mockTokenSource: mockTokenSource{tokens: []string{"t1", "t2", "t3"}},
	}

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", ts.URL, strings.NewReader(`{}`))
	}

	resp, err := client.DoWithAuthRetry(tokenSrc, buildReq, http.DefaultClient.Do)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, int32(3), requestCount.Load(), "should make 3 requests (initial + 401 retry + 403 retry)")
}

// TestClient_RetryLimit_Double401 verifies that two consecutive 401s cause
// the transport to give up (no infinite loop). The second 401 after refresh
// means the token source cannot provide a valid token.
func TestClient_RetryLimit_Double401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()

	tokenSrc := &mockTokenSource{tokens: []string{"bad1", "bad2"}}

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", ts.URL, strings.NewReader(`{}`))
	}

	_, err := client.DoWithAuthRetry(tokenSrc, buildReq, http.DefaultClient.Do)
	require.Error(t, err)

	var authErr *client.ClientAuthError
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, 401, authErr.StatusCode)
}

// TestClient_Streamable_401Integration and TestClient_Streamable_AuthErrorType
// moved to server/auth_retry_integration_test.go (they create servers).

// localStaticToken is a trivial TokenSource for testing.
type localStaticToken struct{ token string }

func (s *localStaticToken) Token() (string, error) { return s.token, nil }
