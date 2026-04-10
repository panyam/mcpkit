package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/panyam/oneauth/client"
	"github.com/panyam/oneauth/core"
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

	// ASMetadataStore caches authorization server metadata across discovery
	// calls. Share a single store across multiple OAuthTokenSource instances
	// connecting to MCP servers behind the same AS to avoid redundant
	// discovery fetches. Optional.
	ASMetadataStore client.ASMetadataStore

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
		if s.ASMetadataStore != nil {
			opts = append(opts, WithASMetadataStore(s.ASMetadataStore))
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
		if err := client.ValidateHTTPS(s.authInfo.ASMetadata); err != nil {
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
	s.Scopes = core.UnionScopes(s.Scopes, scopes)
	s.mu.Unlock()

	return s.Token()
}

// Close stops any background goroutines started by underlying token sources
// (e.g., proactive refresh in ClientCredentialsSource). Safe to call multiple
// times. Implements io.Closer.
//
// After Close, subsequent Token() calls still work — they fall back to
// reactive refresh. Close is typically called from the owning Client.Close()
// on shutdown.
func (s *OAuthTokenSource) Close() error {
	// If oaClient has a client credentials source with proactive refresh,
	// stop it. We check via the optional io.Closer interface to avoid
	// tight coupling to oneauth's concrete types.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oaClient != nil {
		if closer, ok := any(s.oaClient).(interface{ Close() error }); ok {
			return closer.Close()
		}
	}
	return nil
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
		if err := client.ValidateCIMDURL(s.ClientMetadataURL); err != nil {
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
		resp, err := client.RegisterClient(s.authInfo.ASMetadata.RegistrationEndpoint, meta, s.HTTPClient)
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

// Type aliases re-exported from oneauth/client for backward compatibility.
// These types were moved to oneauth as part of mcpkit#158 (generic OAuth pushdown).
type ClientCredentialsSource = client.ClientCredentialsSource
