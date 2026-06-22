package auth

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/panyam/oneauth/client"
	"github.com/panyam/oneauth/utils"
)

// ClientCredentialsTokenSource implements core.TokenSource for the OAuth 2.0
// client_credentials grant (RFC 6749 §4.4, SEP-1046). Use when the MCP client
// is a backend service authenticating with its own identity rather than acting
// on behalf of a human user. The flow has no browser, no consent screen, and
// no refresh_token: tokens are minted directly by the AS in exchange for the
// client's credentials and refetched on expiry.
//
// Two client-authentication variants, selected by which field the caller sets:
//
//   - ClientSecret  (client_secret_basic / client_secret_post per RFC 6749 §2.3.1)
//   - PrivateKeyPEM (private_key_jwt per RFC 7523 §2.2)
//
// PRM discovery, AS metadata fetch, and token caching are inherited from
// oneauth's ClientCredentialsSource; this type adds the MCP-specific glue
// (PRM → AS resolution, scope selection, issuer-as-audience for the JWT
// assertion per RFC 7523bis).
type ClientCredentialsTokenSource struct {
	// ServerURL is the MCP server URL (used for PRM discovery).
	ServerURL string

	// ClientID identifies the client at the AS. Required.
	ClientID string

	// ClientSecret authenticates the client via HTTP Basic / form POST.
	// Mutually exclusive with PrivateKeyPEM.
	ClientSecret string

	// PrivateKeyPEM is the PKCS#8 PEM-encoded private key used to mint
	// the client_assertion JWT. Mutually exclusive with ClientSecret.
	// Must match the public key the AS associates with ClientID.
	PrivateKeyPEM string

	// SigningAlgorithm is the JWS alg for the assertion (e.g. "ES256", "RS256").
	// Required when PrivateKeyPEM is set. Must match the AS's registered
	// signing_alg for this client.
	SigningAlgorithm string

	// KeyID, when non-empty, is emitted as the JWS `kid` header so the AS
	// can pick the right registered key. Optional.
	KeyID string

	// Scopes to request. If empty, inherits from PRM scopes_supported.
	Scopes []string

	// HTTPClient overrides the discovery and token-request HTTP client.
	// Useful in tests with httptest.Server.
	HTTPClient *http.Client

	// AllowInsecure permits http:// AS endpoints. Required for the
	// conformance mock AS; production deployments should leave this false
	// so AS HTTPS is enforced.
	AllowInsecure bool

	// ASMetadataStore caches AS metadata across discovery calls when
	// multiple sources target the same AS.
	ASMetadataStore client.ASMetadataStore

	// OnToken fires after a successful token fetch by the underlying
	// ClientCredentialsSource. Use to persist the credential outside the
	// in-memory cache.
	OnToken func(*client.ServerCredential)

	mu       sync.Mutex
	authInfo *MCPAuthInfo
	source   *client.ClientCredentialsSource
}

// Token returns a cached access token if still valid, or fetches a fresh one
// via the configured client_credentials variant. Implements core.TokenSource.
func (s *ClientCredentialsTokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureSourceLocked(); err != nil {
		return "", err
	}
	return s.source.Token()
}

// TokenForScopes invalidates the cached token, merges the requested scopes
// with the existing set, and fetches a fresh token. Implements
// core.ScopeAwareTokenSource so the mcpkit client transport can step up
// scope on a 403 with insufficient_scope.
func (s *ClientCredentialsTokenSource) TokenForScopes(scopes []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureSourceLocked(); err != nil {
		return "", err
	}
	return s.source.TokenForScopes(scopes)
}

func (s *ClientCredentialsTokenSource) ensureSourceLocked() error {
	if s.source != nil {
		return nil
	}

	if s.ClientID == "" {
		return fmt.Errorf("ClientCredentialsTokenSource: ClientID is required")
	}
	if s.ClientSecret == "" && s.PrivateKeyPEM == "" {
		return fmt.Errorf("ClientCredentialsTokenSource: ClientSecret or PrivateKeyPEM is required")
	}
	if s.ClientSecret != "" && s.PrivateKeyPEM != "" {
		return fmt.Errorf("ClientCredentialsTokenSource: ClientSecret and PrivateKeyPEM are mutually exclusive")
	}
	if s.PrivateKeyPEM != "" && s.SigningAlgorithm == "" {
		return fmt.Errorf("ClientCredentialsTokenSource: SigningAlgorithm is required with PrivateKeyPEM")
	}

	var opts []DiscoverOption
	if s.HTTPClient != nil {
		opts = append(opts, WithHTTPClient(s.HTTPClient))
	}
	if s.ASMetadataStore != nil {
		opts = append(opts, WithASMetadataStore(s.ASMetadataStore))
	}
	info, err := DiscoverMCPAuth(s.ServerURL, opts...)
	if err != nil {
		return fmt.Errorf("MCP auth discovery: %w", err)
	}
	s.authInfo = info

	if !s.AllowInsecure {
		if err := client.ValidateHTTPS(info.ASMetadata); err != nil {
			return err
		}
	}

	scopes := s.Scopes
	if len(scopes) == 0 {
		scopes = info.PRM.ScopesSupported
	}

	issuer := info.AuthorizationServers[0]

	cc := &client.ClientCredentialsSource{
		TokenEndpoint: info.ASMetadata.TokenEndpoint,
		ClientID:      s.ClientID,
		Scopes:        scopes,
		OnToken:       s.OnToken,
	}

	if s.PrivateKeyPEM != "" {
		privKey, err := utils.ParsePrivateKeyPEM([]byte(s.PrivateKeyPEM))
		if err != nil {
			return fmt.Errorf("parse private key: %w", err)
		}
		cc.ClientAssertion = &client.ClientAssertionConfig{
			PrivateKey: privKey,
			SigningAlg: s.SigningAlgorithm,
			KeyID:      s.KeyID,
			Audience:   issuer,
		}
	} else {
		cc.ClientSecret = s.ClientSecret
	}

	s.source = cc
	return nil
}
