package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/panyam/oneauth/client"
)

// JWTBearerTokenSource implements core.TokenSource for the RFC 7523 §2.1
// JWT-bearer authorization grant (SEP-1933 workload identity federation).
// Use when the MCP client is a workload whose identity is attested by a
// trusted issuer out-of-band — a Kubernetes projected service-account
// token, a cloud metadata-service credential, or an upstream IdP-minted
// JWT — and the authorization server exchanges that assertion directly
// for an access token. No browser, no consent screen, no client secret:
// the assertion IS the grant.
//
// This is the authorization GRANT (`assertion` form field), distinct from
// the private_key_jwt client-authentication method (`client_assertion`)
// that ClientCredentialsTokenSource's PrivateKeyPEM variant uses.
//
// PRM discovery and AS metadata resolution follow the same MCP chain as
// the other sources in this package; the token exchange is delegated to
// oneauth's AuthClient.JwtBearerGrant.
type JWTBearerTokenSource struct {
	// ServerURL is the MCP server URL (used for PRM discovery).
	ServerURL string

	// ClientID identifies the workload at the AS. Required.
	ClientID string

	// Assertion is a static signed JWT presented as the grant. Use for
	// assertions whose lifetime exceeds the process (tests, short-lived
	// jobs). Exactly one of Assertion / AssertionProvider must be set.
	Assertion string

	// AssertionProvider returns a fresh assertion per token request. Use
	// when the platform rotates the workload credential (e.g. a projected
	// service-account token file): the provider is consulted on every
	// fetch, so an expired cached assertion is never replayed.
	AssertionProvider func() (string, error)

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

	mu       sync.Mutex
	authInfo *MCPAuthInfo
	auth     *client.AuthClient
	scopes   []string
	cred     *client.ServerCredential
}

// Token returns a cached access token while it remains valid, or performs
// a JWT-bearer grant exchange for a fresh one. Implements core.TokenSource.
// A failed exchange returns the AS's error verbatim and caches nothing —
// per RFC 7523 the client must not retry or fall back to another grant on
// its own; the next call re-attempts only because the caller asked again.
func (s *JWTBearerTokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenLocked()
}

// TokenForScopes merges the requested scopes into the configured set,
// drops the cached credential, and exchanges a fresh assertion. Implements
// core.ScopeAwareTokenSource so the client transport can step up scope
// after a 403 with insufficient_scope.
func (s *JWTBearerTokenSource) TokenForScopes(scopes []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLocked(); err != nil {
		return "", err
	}
	merged := append([]string{}, s.scopes...)
	for _, sc := range scopes {
		found := false
		for _, have := range merged {
			if have == sc {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, sc)
		}
	}
	s.scopes = merged
	s.cred = nil
	return s.tokenLocked()
}

func (s *JWTBearerTokenSource) tokenLocked() (string, error) {
	if err := s.ensureLocked(); err != nil {
		return "", err
	}
	if s.cred != nil && (s.cred.ExpiresAt.IsZero() || time.Now().Before(s.cred.ExpiresAt)) {
		return s.cred.AccessToken, nil
	}

	assertion := s.Assertion
	if s.AssertionProvider != nil {
		var err error
		assertion, err = s.AssertionProvider()
		if err != nil {
			return "", fmt.Errorf("assertion provider: %w", err)
		}
	}

	cred, err := s.auth.JwtBearerGrant(context.Background(), &client.JwtBearerGrantRequest{
		ClientID:  s.ClientID,
		Assertion: assertion,
		Scope:     s.scopes,
	})
	if err != nil {
		return "", fmt.Errorf("jwt-bearer grant: %w", err)
	}
	s.cred = cred
	return cred.AccessToken, nil
}

func (s *JWTBearerTokenSource) ensureLocked() error {
	if s.auth != nil {
		return nil
	}

	if s.ClientID == "" {
		return fmt.Errorf("JWTBearerTokenSource: ClientID is required")
	}
	if s.Assertion == "" && s.AssertionProvider == nil {
		return fmt.Errorf("JWTBearerTokenSource: Assertion or AssertionProvider is required")
	}
	if s.Assertion != "" && s.AssertionProvider != nil {
		return fmt.Errorf("JWTBearerTokenSource: Assertion and AssertionProvider are mutually exclusive")
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

	if !s.AllowInsecure {
		if err := client.ValidateHTTPS(info.ASMetadata); err != nil {
			return err
		}
	}

	s.authInfo = info
	s.scopes = s.Scopes
	if len(s.scopes) == 0 && info.PRM != nil {
		s.scopes = info.PRM.ScopesSupported
	}

	authOpts := []client.ClientOption{client.WithASMetadata(info.ASMetadata)}
	if s.HTTPClient != nil {
		authOpts = append(authOpts, client.WithHTTPClient(s.HTTPClient))
	}
	s.auth = client.NewAuthClient(info.AuthorizationServers[0], nil, authOpts...)
	return nil
}
