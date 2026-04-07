package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/panyam/oneauth/client"
)

// tokenExpiryBuffer is subtracted from token expiry times to account for clock
// skew and network latency. Without this, tokens could expire between the freshness
// check and the server receiving the request, causing spurious 401s.
const tokenExpiryBuffer = 30 * time.Second

// OAuthTokenSource implements core.TokenSource using the full MCP OAuth flow:
// PRM discovery → AS metadata → PKCE authorization code → token.
//
// Per MCP spec (2025-11-25): clients MUST implement PKCE with S256,
// MUST include resource parameter, MUST verify PKCE support in AS metadata.
//
// The discovery flow is:
//  1. Probe MCP server → 401 + WWW-Authenticate header
//  2. Fetch Protected Resource Metadata (PRM, RFC 9728)
//  3. Discover Authorization Server metadata (RFC 8414 / OIDC)
//  4. Validate PKCE S256 support
//  5. Resolve client identity (pre-registered → CIMD → DCR)
//  6. Run PKCE authorization code flow via oneauth LoginWithBrowser
type OAuthTokenSource struct {
	// ServerURL is the MCP server URL (used for PRM discovery and as resource indicator).
	ServerURL string

	// ClientID for pre-registered clients. Takes priority over CIMD and DCR.
	ClientID string

	// ClientSecret for confidential clients (empty for public clients).
	ClientSecret string

	// ClientMetadataURL is a CIMD URL (draft-ietf-oauth-client-id-metadata-document).
	// When set and no ClientID is provided, this URL is used as the client_id.
	// Per MCP spec: SHOULD support Client ID Metadata Documents.
	ClientMetadataURL string

	// EnableDCR enables Dynamic Client Registration (RFC 7591) as a fallback
	// when no ClientID or ClientMetadataURL is set. Per MCP spec: MAY support DCR.
	EnableDCR bool

	// DCRMeta overrides the default DCR registration request.
	// If nil, DefaultClientRegistration() is used.
	DCRMeta *ClientRegistrationRequest

	// Scopes to request. If empty, determined via MCP scope selection strategy
	// (WWW-Authenticate scope > PRM scopes_supported > empty).
	Scopes []string

	// CredStore persists tokens across sessions (optional).
	CredStore client.CredentialStore

	// OpenBrowser opens the authorization URL (nil = platform default).
	OpenBrowser func(url string) error

	// HTTPClient is used for discovery and DCR requests.
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// AllowInsecure skips HTTPS enforcement on AS endpoints.
	// Only set this for local development/testing.
	AllowInsecure bool

	// mu protects token access and discovery state.
	mu       sync.Mutex
	authInfo *MCPAuthInfo
	oaClient *client.AuthClient
	token    string
	expiry   time.Time

	// dcrClientID/dcrClientSecret are cached from a successful DCR call.
	dcrClientID     string
	dcrClientSecret string
}

// Token implements core.TokenSource.
// Returns a cached token if valid, or runs the full MCP OAuth discovery + auth flow.
func (s *OAuthTokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Return cached token if still valid
	if s.token != "" && time.Now().Add(tokenExpiryBuffer).Before(s.expiry) {
		return s.token, nil
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

	// Run MCP discovery (cached after first call)
	if s.authInfo == nil {
		var opts []DiscoverOption
		if s.HTTPClient != nil {
			opts = append(opts, WithHTTPClient(s.HTTPClient))
		}
		info, err := DiscoverMCPAuth(s.ServerURL, opts...)
		if err != nil {
			return "", fmt.Errorf("MCP auth discovery: %w", err)
		}
		s.authInfo = info
	}

	// PKCE pre-flight (C11/C12): MUST verify S256 support, MUST refuse if absent
	if err := ValidatePKCES256(s.authInfo.ASMetadata); err != nil {
		return "", err
	}

	// HTTPS enforcement (X1): AS endpoints MUST be HTTPS
	if !s.AllowInsecure {
		if err := validateHTTPS(s.authInfo.ASMetadata); err != nil {
			return "", err
		}
	}

	// Scope selection: explicit > discovery
	scopes := s.Scopes
	if len(scopes) == 0 {
		scopes = s.authInfo.Scopes
	}

	// Client registration (C6): pre-registered > CIMD > DCR > error
	clientID, clientSecret, err := s.resolveClientID()
	if err != nil {
		return "", fmt.Errorf("client registration: %w", err)
	}

	// Lazy-init the AuthClient with the discovered AS URL
	if s.oaClient == nil {
		issuer := s.authInfo.AuthorizationServers[0]
		var store client.CredentialStore
		if s.CredStore != nil {
			store = s.CredStore
		}
		s.oaClient = client.NewAuthClient(issuer, store)
	}

	// Full browser login flow with explicit endpoints from discovery.
	// Pass TokenEndpointAuthMethods from AS metadata so auth method negotiation
	// works correctly even with explicit endpoints (oneauth#74).
	loginCfg := client.BrowserLoginConfig{
		AuthorizationEndpoint:   s.authInfo.ASMetadata.AuthorizationEndpoint,
		TokenEndpoint:           s.authInfo.ASMetadata.TokenEndpoint,
		TokenEndpointAuthMethods: s.authInfo.ASMetadata.TokenEndpointAuthMethods,
		ClientID:                clientID,
		ClientSecret:            clientSecret,
		Scopes:                  scopes,
		Resource:                s.ServerURL, // RFC 8707: bind token to this MCP server
		OpenBrowser:             s.OpenBrowser,
	}
	if s.HTTPClient != nil {
		loginCfg.HTTPClient = s.HTTPClient
	}
	// ClientSecret is passed in BrowserLoginConfig above — oneauth's
	// SelectAuthMethod handles negotiation (basic/post/none) based on AS metadata.

	cred, err := s.oaClient.LoginWithBrowser(loginCfg)
	if err != nil {
		return "", fmt.Errorf("oauth login: %w", err)
	}

	s.token = cred.AccessToken
	s.expiry = cred.ExpiresAt
	return s.token, nil
}

// TokenForScopes implements core.ScopeAwareTokenSource.
// Invalidates the cached token and triggers a new OAuth flow with the
// requested scopes merged into the existing scope set.
func (s *OAuthTokenSource) TokenForScopes(scopes []string) (string, error) {
	s.mu.Lock()
	s.token = ""
	s.expiry = time.Time{}
	s.Scopes = mergeScopes(s.Scopes, scopes)
	s.mu.Unlock()

	return s.Token()
}

// resolveClientID implements the MCP client registration priority (C6):
// 1. Pre-registered ClientID → use directly
// 2. CIMD ClientMetadataURL → use URL as client_id
// 3. DCR → register dynamically if enabled and AS supports it
// 4. Error
func (s *OAuthTokenSource) resolveClientID() (clientID, clientSecret string, err error) {
	// 1. Pre-registered
	if s.ClientID != "" {
		return s.ClientID, s.ClientSecret, nil
	}

	// 2. CIMD (C7/C8)
	if s.ClientMetadataURL != "" {
		if err := ValidateCIMDURL(s.ClientMetadataURL); err != nil {
			// Log warning but fall through to DCR
			_ = err
		} else {
			return s.ClientMetadataURL, "", nil
		}
	}

	// 3. DCR (C9) — cached after first successful registration
	if s.dcrClientID != "" {
		return s.dcrClientID, s.dcrClientSecret, nil
	}
	if s.EnableDCR && s.authInfo != nil && s.authInfo.ASMetadata != nil &&
		s.authInfo.ASMetadata.RegistrationEndpoint != "" {
		meta := DefaultClientRegistration()
		if s.DCRMeta != nil {
			meta = *s.DCRMeta
		}
		resp, err := RegisterClient(s.authInfo.ASMetadata.RegistrationEndpoint, meta, s.HTTPClient)
		if err != nil {
			return "", "", fmt.Errorf("DCR: %w", err)
		}
		s.dcrClientID = resp.ClientID
		s.dcrClientSecret = resp.ClientSecret
		return resp.ClientID, resp.ClientSecret, nil
	}

	return "", "", errors.New("no client_id: set ClientID, ClientMetadataURL, or EnableDCR")
}

// --- Validation helpers ---

// ValidatePKCES256 checks that the AS supports PKCE with S256 (C11/C12).
// Per MCP spec: clients MUST verify code_challenge_methods_supported includes "S256",
// and MUST refuse to proceed if it is absent.
func ValidatePKCES256(meta *client.ASMetadata) error {
	if meta == nil {
		return errors.New("no AS metadata for PKCE validation")
	}
	for _, m := range meta.CodeChallengeMethodsSupported {
		if strings.EqualFold(m, "S256") {
			return nil
		}
	}
	return fmt.Errorf("AS does not support PKCE S256 (supported: %v)", meta.CodeChallengeMethodsSupported)
}

// validateHTTPS checks that AS endpoints use HTTPS (X1).
// Localhost URLs are exempt for development/testing.
func validateHTTPS(meta *client.ASMetadata) error {
	if meta == nil {
		return nil
	}
	endpoints := []struct{ name, url string }{
		{"authorization_endpoint", meta.AuthorizationEndpoint},
		{"token_endpoint", meta.TokenEndpoint},
	}
	for _, ep := range endpoints {
		if ep.url == "" {
			continue
		}
		if isLocalhost(ep.url) {
			continue
		}
		if !strings.HasPrefix(ep.url, "https://") {
			return fmt.Errorf("AS %s must be HTTPS: %s", ep.name, ep.url)
		}
	}
	return nil
}

// isLocalhost returns true if the URL points to a loopback address.
func isLocalhost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// ValidateCIMDURL validates a Client ID Metadata Document URL (C8).
// Per spec: MUST use https (except localhost for testing), MUST contain a path.
func ValidateCIMDURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid CIMD URL: %w", err)
	}
	if !isLocalhost(rawURL) && u.Scheme != "https" {
		return fmt.Errorf("CIMD URL must use https: %s", rawURL)
	}
	if u.Path == "" || u.Path == "/" {
		return fmt.Errorf("CIMD URL must contain a path: %s", rawURL)
	}
	return nil
}

// --- ClientCredentialsSource (unchanged) ---

// ClientCredentialsSource implements core.TokenSource for machine-to-machine auth.
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

// Token implements core.TokenSource.
func (s *ClientCredentialsSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && time.Now().Add(tokenExpiryBuffer).Before(s.expiry) {
		return s.token, nil
	}

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

// TokenForScopes implements core.ScopeAwareTokenSource.
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
