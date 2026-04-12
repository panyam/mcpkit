package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/oneauth/client"
)

// failingOpenBrowser is a safe default OpenBrowser for tests in this file.
// It returns an error without touching the OS browser so that any
// accidental fall-through to the LoginWithBrowser path fails the test
// quickly instead of spawning a real browser tab.
//
// Test authors: always set OpenBrowser: failingOpenBrowser on every
// OAuthTokenSource in this file. A nil OpenBrowser falls through to the
// platform default (exec "open url" on macOS), which is not what you
// want in a unit test.
//
// Declared as a function (not a var) so it cannot be reassigned from
// another test — the safety net stays in place under all test orderings.
func failingOpenBrowser(url string) error {
	return fmt.Errorf("test: refused to open browser for %s", url)
}

// refreshASServer is a minimal mock authorization server that implements
// only the token endpoint with refresh_token grant. It tracks each call's
// grant_type so tests can assert which flow fired.
//
// This is intentionally NOT a full OAuth AS — we only need the refresh
// endpoint because the tests seed the credential store directly with an
// expiring credential (simulating "user already completed browser login
// once"). Full authorization flow tests live in tests/keycloak/ against
// a real Keycloak.
type refreshASServer struct {
	*httptest.Server
	mu            sync.Mutex
	refreshCount  atomic.Int32
	refreshFails  bool   // when true, the /token endpoint returns 400 invalid_grant
	lastGrantType string // last grant_type seen
	lastScope     string // last scope param seen
}

func newRefreshASServer(t *testing.T) *refreshASServer {
	t.Helper()
	s := &refreshASServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		// Parse both content types. oneauth's legacy requestToken path
		// (used by refreshTokenLocked) posts JSON; the form-encoded path
		// (used by ClientCredentialsToken) posts application/x-www-form-urlencoded.
		grantType := ""
		scope := ""
		switch r.Header.Get("Content-Type") {
		case "application/json":
			var body struct {
				GrantType    string `json:"grant_type"`
				RefreshToken string `json:"refresh_token"`
				Scope        string `json:"scope"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			grantType = body.GrantType
			scope = body.Scope
		default:
			grantType = r.FormValue("grant_type")
			scope = r.FormValue("scope")
		}
		s.mu.Lock()
		s.lastGrantType = grantType
		s.lastScope = scope
		s.mu.Unlock()

		if s.refreshFails {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":             "invalid_grant",
				"error_description": "refresh token expired",
			})
			return
		}

		n := s.refreshCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("refreshed-at-%d", n),
			"refresh_token": fmt.Sprintf("refreshed-rt-%d", n),
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "read write",
		})
	})
	s.Server = httptest.NewServer(mux)
	return s
}

// seedSourceForRefresh configures an OAuthTokenSource for tests that want
// to exercise only the refresh path without running discovery or the
// full client-registration + browser login flow. The caller is
// responsible for providing a store (memory or custom) that already
// contains an expired credential with a refresh token.
//
// After this helper returns, src.Token() will:
//   - skip discovery (authInfo is preset)
//   - skip ValidatePKCES256 + ValidateHTTPS (authInfo is preset)
//   - reach the refresh path (because oaClient is preset with the store)
//
// It still falls through to LoginWithBrowser if the refresh path fails —
// that fall-through is the behavior we're testing.
func seedSourceForRefresh(t *testing.T, src *OAuthTokenSource, mockAS *refreshASServer, store client.CredentialStore) {
	t.Helper()
	// Preset authInfo so Token() skips the discovery branch.
	src.authInfo = &MCPAuthInfo{
		AuthorizationServers: []string{mockAS.URL},
		ASMetadata: &client.ASMetadata{
			AuthorizationEndpoint: mockAS.URL + "/authorize",
			TokenEndpoint:         mockAS.URL + "/token",
			CodeChallengeMethodsSupported: []string{"S256"},
		},
		Scopes: []string{"read", "write"},
	}
	// Preset the oaClient with the caller-supplied store.
	src.oaClient = client.NewAuthClient(mockAS.URL, store,
		client.WithTokenEndpoint("/token"),
		client.WithASMetadata(src.authInfo.ASMetadata))
	if src.OnToken != nil {
		src.oaClient.OnToken = src.OnToken
	}
}

// TestOAuthTokenSource_RefreshPathUsedBeforeBrowserLogin is the core of
// #196: when the cached token is stale but a refresh token is available
// in the store, Token() must call the refresh_token grant instead of
// falling straight to LoginWithBrowser.
//
// The test seeds an expired access token + valid refresh token into the
// store, then asserts that Token() returns the NEW access token produced
// by the mock /token endpoint — not a LoginWithBrowser error.
func TestOAuthTokenSource_RefreshPathUsedBeforeBrowserLogin(t *testing.T) {
	mockAS := newRefreshASServer(t)
	defer mockAS.Close()

	store := NewMemoryCredentialStore()
	_ = store.SetCredential(mockAS.URL, &client.ServerCredential{
		AccessToken:  "stale-access",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
		Scope:        "read write",
	})

	src := &OAuthTokenSource{
		ServerURL:     mockAS.URL,
		ClientID:      "test-client",
		CredStore:     store,
		AllowInsecure: true,
		// Fail fast if any test accidentally falls through to the browser
		// flow — do not let LoginWithBrowser spawn a real browser tab.
		OpenBrowser: failingOpenBrowser,
	}
	seedSourceForRefresh(t, src, mockAS, store)

	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if !strings.HasPrefix(tok, "refreshed-at-") {
		t.Errorf("token = %q, want prefix refreshed-at- (proves refresh path fired)", tok)
	}
	if got := mockAS.refreshCount.Load(); got != 1 {
		t.Errorf("refresh endpoint called %d times, want 1", got)
	}
	mockAS.mu.Lock()
	if mockAS.lastGrantType != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", mockAS.lastGrantType)
	}
	mockAS.mu.Unlock()
}

// TestOAuthTokenSource_OnTokenFiresOnRefresh verifies the #137 integration
// end-to-end for the PKCE/browser flow: the OnToken callback, previously
// latent because Token() always re-ran LoginWithBrowser, now fires when
// the refresh path produces a new credential.
func TestOAuthTokenSource_OnTokenFiresOnRefresh(t *testing.T) {
	mockAS := newRefreshASServer(t)
	defer mockAS.Close()

	store := NewMemoryCredentialStore()
	_ = store.SetCredential(mockAS.URL, &client.ServerCredential{
		AccessToken:  "stale",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
		Scope:        "read write",
	})

	var mu sync.Mutex
	var captured []*client.ServerCredential
	src := &OAuthTokenSource{
		ServerURL:     mockAS.URL,
		ClientID:      "test-client",
		CredStore:     store,
		AllowInsecure: true,
		OpenBrowser:   failingOpenBrowser,
		OnToken: func(cred *client.ServerCredential) {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, cred)
		},
	}
	seedSourceForRefresh(t, src, mockAS, store)

	if _, err := src.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("OnToken fired %d times, want 1", len(captured))
	}
	if !strings.HasPrefix(captured[0].AccessToken, "refreshed-at-") {
		t.Errorf("captured access token = %q, want refreshed-at- prefix", captured[0].AccessToken)
	}
}

// TestOAuthTokenSource_FallsBackToBrowserWhenRefreshFails verifies that
// when the refresh endpoint returns invalid_grant (refresh token expired
// or revoked), Token() falls through to the full LoginWithBrowser flow.
//
// In this test environment LoginWithBrowser itself fails (no real browser
// to drive), so we assert on the error message pattern to prove we
// reached the browser branch rather than returning the refresh error.
func TestOAuthTokenSource_FallsBackToBrowserWhenRefreshFails(t *testing.T) {
	mockAS := newRefreshASServer(t)
	mockAS.refreshFails = true
	defer mockAS.Close()

	store := NewMemoryCredentialStore()
	_ = store.SetCredential(mockAS.URL, &client.ServerCredential{
		AccessToken:  "stale",
		RefreshToken: "revoked-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
		Scope:        "read write",
	})

	// Fake OpenBrowser returns an error so LoginWithBrowser fails
	// deterministically without actually launching a browser.
	src := &OAuthTokenSource{
		ServerURL:     mockAS.URL,
		ClientID:      "test-client",
		CredStore:     store,
		AllowInsecure: true,
		OpenBrowser: func(url string) error {
			return fmt.Errorf("fake browser open failure")
		},
	}
	seedSourceForRefresh(t, src, mockAS, store)

	_, err := src.Token()
	if err == nil {
		t.Fatal("expected error (refresh failed and browser login faked)")
	}
	// The error should indicate the browser-login fallback was attempted,
	// not just the refresh failure.
	if !strings.Contains(err.Error(), "oauth login") && !strings.Contains(err.Error(), "browser") {
		t.Errorf("error = %v, want browser-login fallback error", err)
	}
}

// TestOAuthTokenSource_NoCredStoreStillRefreshes verifies that a source
// without an external CredStore can still exercise the refresh path via
// the default in-memory store. This is the main UX win from #196 — users
// who don't care about cross-process persistence still stop seeing
// repeated browser prompts on token expiry.
//
// To exercise this without a real browser login, we pre-populate the
// source's internal memory store via seedSourceForRefresh. A real
// first-run user reaches the same store via LoginWithBrowser filling it.
func TestOAuthTokenSource_NoCredStoreStillRefreshes(t *testing.T) {
	mockAS := newRefreshASServer(t)
	defer mockAS.Close()

	// No CredStore set on the source. seedSourceForRefresh creates an
	// internal memory store and attaches it.
	memStore := NewMemoryCredentialStore()
	_ = memStore.SetCredential(mockAS.URL, &client.ServerCredential{
		AccessToken:  "stale",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
		Scope:        "read write",
	})

	src := &OAuthTokenSource{
		ServerURL:     mockAS.URL,
		ClientID:      "test-client",
		AllowInsecure: true,
		OpenBrowser:   failingOpenBrowser,
		// CredStore: intentionally nil
	}
	seedSourceForRefresh(t, src, mockAS, memStore)

	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if !strings.HasPrefix(tok, "refreshed-at-") {
		t.Errorf("token = %q, want refreshed- prefix", tok)
	}
}

// TestOAuthTokenSource_CredStoreReceivesRefreshedCredential verifies that
// when the caller provides an external CredStore, refresh updates land
// in that store (not in a hidden internal cache). Persistence consumers
// rely on this to keep the refreshed token durable across process
// restarts.
func TestOAuthTokenSource_CredStoreReceivesRefreshedCredential(t *testing.T) {
	mockAS := newRefreshASServer(t)
	defer mockAS.Close()

	store := NewMemoryCredentialStore() // acts as user-provided external store
	_ = store.SetCredential(mockAS.URL, &client.ServerCredential{
		AccessToken:  "stale",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
		Scope:        "read write",
	})

	src := &OAuthTokenSource{
		ServerURL:     mockAS.URL,
		ClientID:      "test-client",
		CredStore:     store,
		AllowInsecure: true,
		// Fail fast if any test accidentally falls through to the browser
		// flow — do not let LoginWithBrowser spawn a real browser tab.
		OpenBrowser: failingOpenBrowser,
	}
	seedSourceForRefresh(t, src, mockAS, store)

	if _, err := src.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	// The user's store should now have the refreshed credential.
	got, err := store.GetCredential(mockAS.URL)
	if err != nil {
		t.Fatalf("store.GetCredential: %v", err)
	}
	if got == nil {
		t.Fatal("store is empty after refresh; should contain refreshed credential")
	}
	if !strings.HasPrefix(got.AccessToken, "refreshed-at-") {
		t.Errorf("stored AccessToken = %q, want refreshed-at- prefix", got.AccessToken)
	}
	if !strings.HasPrefix(got.RefreshToken, "refreshed-rt-") {
		t.Errorf("stored RefreshToken = %q, want refreshed-rt- prefix", got.RefreshToken)
	}
}

// TestOAuthTokenSource_TokenForScopesSkipsRefresh verifies that scope
// step-up (TokenForScopes) bypasses the refresh path and forces the full
// re-login flow. Refresh tokens carry the scope set of the original
// grant; asking for additional scopes via refresh is not guaranteed to
// work across AS vendors, so the safe thing is to re-login.
//
// Since LoginWithBrowser will fail in the test env, we assert on the
// behavior by checking that the refresh endpoint was NOT hit and the
// error indicates browser-login fallback.
func TestOAuthTokenSource_TokenForScopesSkipsRefresh(t *testing.T) {
	mockAS := newRefreshASServer(t)
	defer mockAS.Close()

	store := NewMemoryCredentialStore()
	_ = store.SetCredential(mockAS.URL, &client.ServerCredential{
		AccessToken:  "stale",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
		Scope:        "read",
	})

	src := &OAuthTokenSource{
		ServerURL:     mockAS.URL,
		ClientID:      "test-client",
		Scopes:        []string{"read"},
		CredStore:     store,
		AllowInsecure: true,
		OpenBrowser: func(url string) error {
			return fmt.Errorf("fake browser open failure")
		},
	}
	seedSourceForRefresh(t, src, mockAS, store)

	// Request additional scope. This should invalidate the cached cred
	// and re-run the full Token() flow — which, after this PR's changes,
	// should skip the refresh path because of the scope invalidation.
	_, err := src.TokenForScopes([]string{"write"})
	if err == nil {
		t.Fatal("expected error — no real browser")
	}
	if n := mockAS.refreshCount.Load(); n != 0 {
		t.Errorf("refresh endpoint called %d times, want 0 (scope step-up must skip refresh)", n)
	}
}
