package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	mcpcore "github.com/panyam/mcpkit/core"
	oneauthclient "github.com/panyam/oneauth/client"
	"github.com/panyam/oneauth/testutil"
)

// TestOAuthTokenSource_DefersUntilArmed verifies the issue-818 lazy gate: a
// source with no explicit Scopes and no server challenge yet returns
// core.ErrNoTokenYet and does NOT run discovery or open a browser. Before the
// fix, Token() ran discovery eagerly and would fail with a discovery error
// (not ErrNoTokenYet) against the unreachable server.
func TestOAuthTokenSource_DefersUntilArmed(t *testing.T) {
	src := &OAuthTokenSource{
		ServerURL: "http://127.0.0.1:1/mcp",
		ClientID:  "test-cli",
		OpenBrowser: func(string) error {
			t.Fatal("OpenBrowser must not be called before the source is armed")
			return nil
		},
	}

	tok, err := src.Token()
	if !errors.Is(err, mcpcore.ErrNoTokenYet) {
		t.Fatalf("Token() = (%q, %v), want core.ErrNoTokenYet", tok, err)
	}
	if tok != "" {
		t.Fatalf("Token() returned non-empty token %q while deferring", tok)
	}
}

// TestOAuthTokenSource_ExplicitScopesBypassLazyGate verifies that a caller who
// pins Scopes opts out of laziness — Token() proceeds to discovery (which here
// fails against an unreachable server) rather than deferring with
// ErrNoTokenYet.
func TestOAuthTokenSource_ExplicitScopesBypassLazyGate(t *testing.T) {
	src := &OAuthTokenSource{
		ServerURL:     "http://127.0.0.1:1/mcp",
		ClientID:      "test-cli",
		Scopes:        []string{"mcp:basic"},
		AllowInsecure: true,
		OpenBrowser:   func(string) error { return nil },
	}

	_, err := src.Token()
	if errors.Is(err, mcpcore.ErrNoTokenYet) {
		t.Fatal("explicit Scopes must bypass the lazy gate, got ErrNoTokenYet")
	}
	if err == nil {
		t.Fatal("expected a discovery error against the unreachable server")
	}
}

// TestOAuthTokenSource_ChallengeScopeWinsOverPRM is the core issue-818
// acceptance: when a 401 challenge selects scope "mcp:basic" but the PRM
// advertises scopes_supported=["mcp:profile"], the source's first (and only)
// authorization request carries the challenge scope, not the PRM catalog.
//
// The flow is driven through the real oneauth authorization-code + PKCE path
// against oneauth's reusable test AS (testutil.NewTestAuthServer). The scope
// the source requests is captured from the authorization URL handed to
// OpenBrowser before FollowRedirects completes the flow.
func TestOAuthTokenSource_ChallengeScopeWinsOverPRM(t *testing.T) {
	as := testutil.NewTestAuthServer(t,
		testutil.WithAuthorizeEnabled(true),
		testutil.WithAuthorizeAutoApproveSubject("test-user"),
		testutil.WithScopes([]string{"mcp:basic", "mcp:profile", "mcp:write"}),
	)

	mcp := newPRMOnlyMCPServer(t, as.URL(), []string{"mcp:profile"})
	defer mcp.Close()

	var (
		mu              sync.Mutex
		authorizeScopes []string
	)
	recordScope := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		mu.Lock()
		authorizeScopes = append(authorizeScopes, u.Query().Get("scope"))
		mu.Unlock()
		return oneauthclient.FollowRedirects(nil)(authURL)
	}

	src := &OAuthTokenSource{
		ServerURL:     mcp.URL + "/mcp",
		ClientID:      "test-cli",
		AllowInsecure: true,
		OpenBrowser:   recordScope,
	}

	// Arm with the per-operation challenge scope, as the transport's
	// OnUnauthorized would after a 401 carrying scope="mcp:basic".
	tok, err := src.TokenForScopes([]string{"mcp:basic"})
	if err != nil {
		t.Fatalf("TokenForScopes: %v", err)
	}
	if tok == "" {
		t.Fatal("expected a non-empty access token")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(authorizeScopes) != 1 {
		t.Fatalf("expected exactly 1 authorization request, got %d: %v", len(authorizeScopes), authorizeScopes)
	}
	got := authorizeScopes[0]
	if !scopeContains(got, "mcp:basic") {
		t.Errorf("authorization scope = %q, want it to include the challenge scope mcp:basic", got)
	}
	if scopeContains(got, "mcp:profile") {
		t.Errorf("authorization scope = %q, must NOT include the PRM catalog scope mcp:profile", got)
	}
}

// TestOAuthTokenSource_PRMFallbackWhenChallengeHasNoScope verifies the
// scenario-2 path preserved by issue 818: when a 401 carries no scope=, the
// transport arms the source with an empty TokenForScopes call and the
// subsequent acquisition falls back to the PRM scopes_supported catalog.
func TestOAuthTokenSource_PRMFallbackWhenChallengeHasNoScope(t *testing.T) {
	as := testutil.NewTestAuthServer(t,
		testutil.WithAuthorizeEnabled(true),
		testutil.WithAuthorizeAutoApproveSubject("test-user"),
		testutil.WithScopes([]string{"mcp:basic", "mcp:read", "mcp:write"}),
	)

	prmScopes := []string{"mcp:basic", "mcp:read", "mcp:write"}
	mcp := newPRMOnlyMCPServer(t, as.URL(), prmScopes)
	defer mcp.Close()

	var (
		mu              sync.Mutex
		authorizeScopes []string
	)
	recordScope := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		mu.Lock()
		authorizeScopes = append(authorizeScopes, u.Query().Get("scope"))
		mu.Unlock()
		return oneauthclient.FollowRedirects(nil)(authURL)
	}

	src := &OAuthTokenSource{
		ServerURL:     mcp.URL + "/mcp",
		ClientID:      "test-cli",
		AllowInsecure: true,
		OpenBrowser:   recordScope,
	}

	// Empty challenge (no scope= on the 401) — arms the source for the PRM
	// fallback without contributing any scope of its own.
	if _, err := src.TokenForScopes(nil); err != nil {
		t.Fatalf("TokenForScopes(nil): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(authorizeScopes) != 1 {
		t.Fatalf("expected exactly 1 authorization request, got %d: %v", len(authorizeScopes), authorizeScopes)
	}
	got := authorizeScopes[0]
	for _, want := range prmScopes {
		if !scopeContains(got, want) {
			t.Errorf("authorization scope = %q, want PRM fallback to include %q", got, want)
		}
	}
}

// newPRMOnlyMCPServer returns an httptest server that stands in for an MCP
// resource server during discovery: the POST probe answers 401 pointing at its
// PRM, and the PRM document advertises the given authorization server and
// scopes_supported. It issues no tokens — acquisition happens at the AS.
func newPRMOnlyMCPServer(t *testing.T, asURL string, scopesSupported []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var serverURL string
	srv := httptest.NewServer(mux)
	serverURL = srv.URL

	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              serverURL + "/mcp",
			"authorization_servers": []string{asURL},
			"scopes_supported":      scopesSupported,
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate",
			`Bearer resource_metadata="`+serverURL+`/.well-known/oauth-protected-resource/mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	return srv
}

// scopeContains reports whether a space-separated OAuth scope string includes
// the given scope token (RFC 6749 §3.3).
func scopeContains(scope, want string) bool {
	for _, s := range strings.Fields(scope) {
		if s == want {
			return true
		}
	}
	return false
}
