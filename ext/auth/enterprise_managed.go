package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/panyam/oneauth/client"
)

// EnterpriseManagedTokenSource implements core.TokenSource for the SEP-990
// enterprise-managed authorization flow. The MCP client presents an upstream
// IdP-issued ID token, exchanges it for an ID-JAG via RFC 8693 at the IdP,
// then redeems the ID-JAG at the MCP authorization server via RFC 7523 §2.1
// (jwt-bearer grant) using `client_secret_basic` for client authentication.
//
// This is the two-stage chain in plain terms:
//
//	IdP /token   (RFC 8693)   id_token → id-jag
//	AS  /token   (RFC 7523)   id-jag   → MCP access token
//
// The first stage is "the IdP vouches for the user". The second stage is
// "the AS vouches for the MCP client acting on the user's behalf at the
// MCP server." See:
//
//	https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/enterprise-managed-authorization.mdx
//
// Token caching is keyed on the MCP access token's expiry. When the access
// token expires the source re-runs the full two-stage chain — the ID-JAG
// is single-use so we never cache it across calls.
type EnterpriseManagedTokenSource struct {
	// ServerURL is the MCP server URL. Used for PRM discovery and as the
	// `resource` form value on the IdP token exchange.
	ServerURL string

	// ClientID identifies this MCP client at the MCP authorization server.
	// Required.
	ClientID string

	// ClientSecret authenticates the MCP client at the AS via
	// `client_secret_basic` (RFC 6749 §2.3.1) on the jwt-bearer grant
	// request. Required when ClientID is set (confidential client); leave
	// empty when using ClientMetadataURL (public CIMD client).
	ClientSecret string

	// ClientMetadataURL is a CIMD (Client ID Metadata Document) URL
	// (draft-ietf-oauth-client-id-metadata-document). When set and ClientID
	// is empty, this URL is used as the client_id — a public client with no
	// secret. This is the DCR-free client-identity path from ID-JAG draft §5:
	// a globally-resolvable client_id the IdP can embed in the ID-JAG without
	// the client having pre-registered at the MCP AS. ClientID
	// (pre-registration) takes priority when both are set.
	ClientMetadataURL string

	// IdpClientID is the client identity sent on the IdP token exchange.
	// SEP-990 §6.1 notes the IdP "needs to be aware of the MCP Client's
	// client_id that it normally uses with the MCP Server"; this field
	// names the client at the IdP, which may differ from ClientID above.
	// Optional — empty falls back to the unauthenticated token-exchange
	// path (AuthMethodNone), which the conformance fixture accepts. When
	// empty and ClientMetadataURL is set, the CIMD URL is used as the
	// IdP-side client_id too, keeping one global identity across both stages.
	IdpClientID string

	// IdpIDToken is the signed ID token issued by the IdP for the user.
	// Required. Sent as the RFC 8693 `subject_token`.
	IdpIDToken string

	// IdpTokenEndpoint is the URL of the IdP token endpoint that performs
	// the RFC 8693 token exchange. Required. The conformance scenario
	// provides this directly in MCP_CONFORMANCE_CONTEXT rather than
	// requiring a separate IdP discovery step.
	IdpTokenEndpoint string

	// AllowInsecure permits http:// AS endpoints. The conformance mock AS
	// uses HTTP; production deployments leave this false.
	AllowInsecure bool

	// HTTPClient overrides the discovery and token-request HTTP client.
	HTTPClient *http.Client

	// ASMetadataStore caches AS metadata across discovery calls.
	ASMetadataStore client.ASMetadataStore

	mu          sync.Mutex
	authInfo    *MCPAuthInfo
	accessToken string
	expiry      time.Time
}

// Token returns a cached access token if still valid, or runs the full
// two-stage chain (token exchange at IdP → jwt-bearer grant at AS).
// Implements core.TokenSource.
func (s *EnterpriseManagedTokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.accessToken != "" && time.Now().Add(tokenExpiryBuffer).Before(s.expiry) {
		return s.accessToken, nil
	}
	if err := s.ensureDiscoveredLocked(); err != nil {
		return "", err
	}
	return s.refetchLocked()
}

// TokenForScopes invalidates the cached access token and re-runs the
// chain. SEP-990 doesn't have a scope step-up story today; the implementation
// re-runs the same flow because the AS may issue a token with different
// scope based on the (already-issued) ID-JAG.
func (s *EnterpriseManagedTokenSource) TokenForScopes(_ []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.accessToken = ""
	s.expiry = time.Time{}
	if err := s.ensureDiscoveredLocked(); err != nil {
		return "", err
	}
	return s.refetchLocked()
}

func (s *EnterpriseManagedTokenSource) ensureDiscoveredLocked() error {
	if s.authInfo != nil {
		return nil
	}
	if _, _, err := s.resolveClientID(); err != nil {
		return err
	}
	if s.IdpIDToken == "" {
		return fmt.Errorf("EnterpriseManagedTokenSource: IdpIDToken is required")
	}
	if s.IdpTokenEndpoint == "" {
		return fmt.Errorf("EnterpriseManagedTokenSource: IdpTokenEndpoint is required")
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
	return nil
}

// resolveClientID resolves the MCP client's identity at the MCP authorization
// server, in priority order:
//  1. Pre-registered ClientID (with ClientSecret) — a confidential client.
//  2. ClientMetadataURL — a CIMD URL used as the client_id with no secret (a
//     public client). Enables the DCR-free path in ID-JAG draft §5.
//
// Returns an error if neither is set, or if the CIMD URL is malformed. Mirrors
// OAuthTokenSource.resolveClientID (minus DCR, which the EMA flow does not use).
func (s *EnterpriseManagedTokenSource) resolveClientID() (clientID, clientSecret string, err error) {
	if s.ClientID != "" {
		if s.ClientSecret == "" {
			return "", "", fmt.Errorf("EnterpriseManagedTokenSource: ClientSecret is required when ClientID is set")
		}
		return s.ClientID, s.ClientSecret, nil
	}
	if s.ClientMetadataURL != "" {
		if err := client.ValidateCIMDURL(s.ClientMetadataURL); err != nil {
			return "", "", fmt.Errorf("EnterpriseManagedTokenSource: invalid ClientMetadataURL: %w", err)
		}
		return s.ClientMetadataURL, "", nil
	}
	return "", "", fmt.Errorf("EnterpriseManagedTokenSource: client identity required — set ClientID (with ClientSecret) or ClientMetadataURL")
}

func (s *EnterpriseManagedTokenSource) refetchLocked() (string, error) {
	asIssuer := s.authInfo.AuthorizationServers[0]
	asTokenEndpoint := s.authInfo.ASMetadata.TokenEndpoint

	clientID, clientSecret, err := s.resolveClientID()
	if err != nil {
		return "", err
	}
	// The IdP-side client_id defaults to the CIMD URL when IdpClientID is
	// unset, so a single global client identity flows through both stages.
	idpClientID := s.IdpClientID
	if idpClientID == "" {
		idpClientID = s.ClientMetadataURL
	}

	// Stage 1 — IdP RFC 8693 token exchange. ClientSecret/ClientAssertion
	// are intentionally left empty: SEP-990 has the IdP authorize on the
	// strength of the subject_token's signature + claims, not via an
	// additional client credential at the exchange endpoint.
	idp := client.NewAuthClient(s.IdpTokenEndpoint, nil,
		client.WithASMetadata(&client.ASMetadata{TokenEndpoint: s.IdpTokenEndpoint}))

	// oneauth 0.1.9 (#217): TokenExchange now takes (ctx, *TokenExchangeRequest).
	exch, err := idp.TokenExchange(context.Background(), &client.TokenExchangeRequest{
		ClientID:           idpClientID,
		SubjectToken:       s.IdpIDToken,
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:id_token",
		RequestedTokenType: "urn:ietf:params:oauth:token-type:id-jag",
		Audience:           []string{asIssuer},
		Resource:           []string{s.ServerURL},
	})
	if err != nil {
		return "", fmt.Errorf("idp token exchange: %w", err)
	}
	if exch.AccessToken == "" {
		return "", fmt.Errorf("idp token exchange returned no access_token")
	}

	// Stage 2 — AS RFC 7523 §2.1 jwt-bearer grant. A pre-registered ClientID
	// authenticates via client_secret_basic; a CIMD client sends client_id
	// only (oneauth's SelectAuthMethod picks AuthMethodNone on empty secret).
	as := client.NewAuthClient(asTokenEndpoint, nil,
		client.WithASMetadata(&client.ASMetadata{
			TokenEndpoint:            asTokenEndpoint,
			TokenEndpointAuthMethods: s.authInfo.ASMetadata.TokenEndpointAuthMethods,
		}))

	cred, err := as.JwtBearerGrant(context.Background(), &client.JwtBearerGrantRequest{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Assertion:    exch.AccessToken,
	})
	if err != nil {
		return "", fmt.Errorf("as jwt-bearer grant: %w", err)
	}

	s.accessToken = cred.AccessToken
	s.expiry = cred.ExpiresAt
	return s.accessToken, nil
}
