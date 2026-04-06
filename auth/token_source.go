package auth

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/panyam/oneauth/client"
)

// tokenExpiryBuffer is subtracted from token expiry times to account for clock
// skew and network latency. Without this, tokens could expire between the freshness
// check and the server receiving the request, causing spurious 401s.
const tokenExpiryBuffer = 30 * time.Second

// OAuthTokenSource implements mcpkit.TokenSource using oneauth's AuthClient.
// It handles the full MCP OAuth flow: cached token → refresh → discovery → browser auth.
//
// Per MCP spec (2025-11-25): clients MUST implement PKCE with S256,
// MUST include resource parameter, MUST verify PKCE support in AS metadata.
type OAuthTokenSource struct {
	// ServerURL is the MCP server URL (used for PRM discovery and as resource indicator).
	ServerURL string

	// ClientID for pre-registered clients or CIMD URL.
	ClientID string

	// ClientSecret for confidential clients (empty for public clients).
	ClientSecret string

	// Scopes to request. If empty, determined via MCP scope selection strategy.
	Scopes []string

	// CredStore persists tokens across sessions (optional).
	CredStore client.CredentialStore

	// OpenBrowser opens the authorization URL (nil = platform default).
	OpenBrowser func(url string) error

	// mu protects token access.
	mu     sync.Mutex
	client *client.AuthClient
	token  string
	expiry time.Time
}

// Token implements mcpkit.TokenSource.
// Returns a cached token if valid, refreshes if expired, or runs the full
// OAuth discovery + PKCE flow.
func (s *OAuthTokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Return cached token if still valid (with buffer for clock skew / network latency)
	if s.token != "" && time.Now().Add(tokenExpiryBuffer).Before(s.expiry) {
		return s.token, nil
	}

	// Lazy-init the AuthClient
	if s.client == nil {
		var store client.CredentialStore
		if s.CredStore != nil {
			store = s.CredStore
		}
		s.client = client.NewAuthClient(s.ServerURL, store)
	}

	// Try loading from credential store
	if s.CredStore != nil {
		cred, err := s.CredStore.GetCredential(s.ServerURL)
		if err == nil && cred != nil && !cred.IsExpired() {
			s.token = cred.AccessToken
			s.expiry = cred.ExpiresAt
			return s.token, nil
		}
	}

	// Full browser login flow
	// Per MCP spec (2025-11-25): MUST include resource parameter (RFC 8707),
	// oneauth#66 adds Resource field; oneauth#65 verifies PKCE S256 support.
	cred, err := s.client.LoginWithBrowser(client.BrowserLoginConfig{
		ClientID:    s.ClientID,
		Scopes:      s.Scopes,
		Resource:    s.ServerURL, // RFC 8707: bind token to this MCP server
		OpenBrowser: s.OpenBrowser,
	})
	if err != nil {
		return "", fmt.Errorf("oauth login: %w", err)
	}

	s.token = cred.AccessToken
	s.expiry = cred.ExpiresAt
	return s.token, nil
}

// TokenForScopes implements mcpkit.ScopeAwareTokenSource.
// Invalidates the cached token and triggers a new OAuth flow with the
// requested scopes merged into the existing scope set.
func (s *OAuthTokenSource) TokenForScopes(scopes []string) (string, error) {
	s.mu.Lock()
	// Invalidate cached token
	s.token = ""
	s.expiry = time.Time{}
	// Merge scopes
	s.Scopes = mergeScopes(s.Scopes, scopes)
	s.mu.Unlock()

	return s.Token()
}

// ClientCredentialsSource implements mcpkit.TokenSource for machine-to-machine auth.
// Uses the OAuth client_credentials grant (RFC 6749 §4.4).
//
// Per MCP spec extensions: io.modelcontextprotocol/oauth-client-credentials
type ClientCredentialsSource struct {
	// TokenEndpoint is the authorization server's token URL.
	TokenEndpoint string

	// ClientID identifies this client to the authorization server.
	ClientID string

	// ClientSecret authenticates this client.
	ClientSecret string

	// Scopes to request.
	Scopes []string

	// Audience is the MCP server's canonical URI (RFC 8707 resource indicator).
	Audience string

	mu     sync.Mutex
	client *client.AuthClient
	token  string
	expiry time.Time
}

// Token implements mcpkit.TokenSource.
func (s *ClientCredentialsSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Return cached token if still valid (with buffer for clock skew / network latency)
	if s.token != "" && time.Now().Add(tokenExpiryBuffer).Before(s.expiry) {
		return s.token, nil
	}

	// Lazy-init
	if s.client == nil {
		s.client = client.NewAuthClient(s.TokenEndpoint, nil)
	}

	cred, err := s.client.ClientCredentialsToken(s.ClientID, s.ClientSecret, s.Scopes)
	if err != nil {
		return "", fmt.Errorf("client credentials: %w", err)
	}

	s.token = cred.AccessToken
	s.expiry = cred.ExpiresAt
	return s.token, nil
}

// TokenForScopes implements mcpkit.ScopeAwareTokenSource.
// Invalidates the cached token and triggers a new client_credentials flow
// with the requested scopes merged into the existing scope set.
func (s *ClientCredentialsSource) TokenForScopes(scopes []string) (string, error) {
	s.mu.Lock()
	s.token = ""
	s.expiry = time.Time{}
	s.Scopes = mergeScopes(s.Scopes, scopes)
	s.mu.Unlock()

	return s.Token()
}

// mergeScopes returns the union of existing and required scopes, sorted.
func mergeScopes(existing, required []string) []string {
	set := make(map[string]struct{}, len(existing)+len(required))
	for _, s := range existing {
		set[s] = struct{}{}
	}
	for _, s := range required {
		set[s] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for s := range set {
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}
