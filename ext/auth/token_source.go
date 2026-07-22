package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpcore "github.com/panyam/mcpkit/core"
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
// Acquisition is lazy: the source defers the OAuth flow until a server
// challenge selects the scope (see Token). The flow, once a challenge
// arms the source, is:
//  1. Fetch Protected Resource Metadata (PRM, RFC 9728)
//  2. Discover Authorization Server metadata (RFC 8414 / OIDC)
//  3. Validate PKCE S256 support
//  4. Resolve client identity (pre-registered → CIMD → DCR)
//  5. Run PKCE authorization code flow via oneauth LoginWithBrowser
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

	// Scopes to request. Leave empty for the default lazy behavior: the
	// source acquires no token until a server 401/403 selects the scope via
	// WWW-Authenticate, then uses that (falling back to PRM scopes_supported
	// only when the challenge carries no scope=). Setting Scopes explicitly
	// pins them and acquires eagerly on the first Token call. TokenForScopes
	// merges challenge scopes into this set as step-up occurs.
	Scopes []string

	// CredStore persists tokens across sessions (optional).
	CredStore client.CredentialStore

	// ASMetadataStore caches authorization server metadata across discovery
	// calls. Share a single store across multiple OAuthTokenSource instances
	// connecting to MCP servers behind the same AS to avoid redundant
	// discovery fetches. Optional.
	ASMetadataStore client.ASMetadataStore

	// OnToken is an optional callback invoked after a successful token
	// acquisition or refresh by the underlying oneauth AuthClient. Use
	// this to persist tokens to an external store (disk, database, secret
	// manager) without implementing a full CredentialStore.
	//
	// Fires ONLY for the refresh_token grant path in the underlying
	// AuthClient — not for initial LoginWithBrowser calls in today's
	// OAuthTokenSource.Token() flow (which re-runs the full browser flow
	// instead of using refresh tokens). This is a latent capability until
	// the browser-login flow learns to use refresh tokens (follow-up).
	//
	// Thread safety: the callback is invoked synchronously while the
	// underlying AuthClient mutex is held (same contract as
	// CredentialStore.SetCredential). Callbacks must not re-enter
	// AuthClient or OAuthTokenSource methods, or they will deadlock.
	//
	// See oneauth#82 for the underlying pushdown. Issue #137.
	OnToken func(*client.ServerCredential)

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

	// armed reports whether acquisition has been authorized by a server
	// challenge (TokenForScopes, called by the transport on a 401/403) or
	// by the caller setting explicit Scopes. Until armed, Token defers and
	// returns core.ErrNoTokenYet so the first request goes out
	// unauthenticated and the server's WWW-Authenticate challenge selects
	// the scope (RFC 6750 §3.1). Mutated only under mu. Invalidate
	// deliberately leaves it set so an in-flight step-up retry still
	// acquires rather than re-deferring.
	armed bool

	// memoryStore is the default backing store for the AuthClient when
	// s.CredStore is nil. Allocated lazily on first Token() call; holds
	// refresh tokens between Token() invocations so the refresh path can
	// fire without forcing external persistence. Mutated only under s.mu.
	memoryStore *MemoryCredentialStore

	// dcrClientID/dcrClientSecret are cached from a successful DCR call.
	// dcrAS is the authorization-server URL the DCR was performed
	// against — used by resolveClientID to detect SEP-2352 AS migration
	// (a server's PRM authorization_servers changes mid-flight) and
	// drop the cache so the client re-registers at the new AS rather
	// than presenting AS₁'s client_id to AS₂.
	dcrClientID     string
	dcrClientSecret string
	dcrAS           string
}

// Invalidate implements core.InvalidatingTokenSource. It drops cached
// discovery, the cached access token, the AuthClient handle, and the
// in-memory credential store entry, so the next Token call re-runs
// the full discovery + auth flow. DoWithAuthRetry calls this before
// re-issuing Token on a 401 — necessary for SEP-2352 AS-change
// re-discovery to actually fire, since the cached authInfo would
// otherwise mask the AS swap.
//
// DCR client credentials are intentionally NOT cleared here.
// resolveClientID handles that lazily, comparing the current AS
// against the cached dcrAS and dropping the cache only on mismatch.
// This keeps Invalidate cheap for the common case (same AS, fresh
// token needed) while still meeting SEP-2352 when the AS truly
// changes.
//
// The armed flag (Token's lazy gate) is intentionally NOT reset: the
// transport calls Invalidate then TokenForScopes on a 401, and re-arming
// would be redundant, but more importantly an already-armed source must
// stay armed across a step-up retry so the re-issued Token acquires
// rather than re-deferring with core.ErrNoTokenYet.
//
// Safe to call multiple times.
func (s *OAuthTokenSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authInfo = nil
	s.token = ""
	s.expiry = time.Time{}
	s.oaClient = nil
	if s.memoryStore != nil {
		// Drop the cached credential so the next Token() can't shortcut
		// via the (2) CredStore path before discovery re-runs.
		_ = s.memoryStore.RemoveCredential(s.ServerURL)
	}
}

// Token implements core.TokenSource.
//
// Acquisition is LAZY by default: until a server challenge selects the
// scope, Token returns ("", core.ErrNoTokenYet) instead of running the
// OAuth flow. The transport treats that sentinel as "send this request
// without an Authorization header", the server replies 401 with a
// per-operation WWW-Authenticate scope=, and the auth-retry path calls
// TokenForScopes to arm the source and acquire with that exact scope
// (RFC 6750 §3.1). This avoids pre-acquiring with the broader PRM
// scopes_supported catalog before any operation has stated what it needs.
// Setting explicit Scopes on the source opts out of laziness — a caller
// that pins scopes acquires eagerly on the first Token call.
//
// Once armed (or with explicit Scopes), attempts are ordered cheap-to-expensive:
//  1. In-memory cached token still valid                        -> return it
//  2. CredStore has a non-expired credential (fast path)        -> cache + return
//  3. Refresh path: if the stored credential has a refresh token
//     and the scope set still covers s.Scopes, exchange it for a
//     new access token via AuthClient.GetToken -> refreshTokenLocked
//  4. Full LoginWithBrowser flow (opens a browser tab)
//
// The refresh path requires a non-nil CredentialStore on the
// AuthClient; when s.CredStore is nil, an internal in-memory store is
// used so refresh tokens issued by LoginWithBrowser can be exercised on
// subsequent calls without forcing external persistence.
func (s *OAuthTokenSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// (1) Return cached token if still valid.
	if s.token != "" && time.Now().Add(tokenExpiryBuffer).Before(s.expiry) {
		return s.token, nil
	}

	// (2) Try loading from the external credential store.
	if s.CredStore != nil {
		cred, err := s.CredStore.GetCredential(s.ServerURL)
		if err == nil && cred != nil && !cred.IsExpired() && credentialCoversScopes(cred, s.Scopes) {
			s.token = cred.AccessToken
			s.expiry = cred.ExpiresAt
			return s.token, nil
		}
	}

	// Lazy gate: defer acquisition until a server challenge has armed us
	// (via TokenForScopes on a 401/403) or the caller pinned explicit
	// Scopes. Returning core.ErrNoTokenYet here makes the transport send
	// the next request unauthenticated, so the server's WWW-Authenticate
	// scope= drives selection instead of the PRM scopes_supported catalog
	// (RFC 6750 §3.1). Issue 818.
	if !s.armed && len(s.Scopes) == 0 {
		return "", mcpcore.ErrNoTokenYet
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

	// Scope selection: explicit/challenge-accumulated (s.Scopes) > PRM
	// scopes_supported catalog fallback. s.Scopes carries both the caller's
	// explicit scopes and any challenge scopes merged in by TokenForScopes,
	// so a per-operation WWW-Authenticate scope (set just before this call
	// on a 401/403) always wins over the broader PRM catalog. Empty on both
	// means omit the scope parameter entirely (RFC 6749 §3.3).
	scopes := s.Scopes
	if len(scopes) == 0 && s.authInfo.PRM != nil {
		scopes = s.authInfo.PRM.ScopesSupported
	}

	// Client registration (C6): pre-registered > CIMD > DCR > error
	clientID, clientSecret, err := s.resolveClientID()
	if err != nil {
		return "", fmt.Errorf("client registration: %w", err)
	}

	// Lazy-init the AuthClient with the discovered AS URL. Always supply a
	// non-nil store so AuthClient.GetToken (the refresh path) has
	// somewhere to read the current credential from; fall back to an
	// internal MemoryCredentialStore when no external CredStore is set.
	// Pass through the OnToken callback so refresh_token grant exchanges
	// fire the consumer's persistence hook (#137, oneauth#82).
	if s.oaClient == nil {
		issuer := s.authInfo.AuthorizationServers[0]
		store := s.credStore()
		s.oaClient = client.NewAuthClient(issuer, store)
		if s.OnToken != nil {
			s.oaClient.OnToken = s.OnToken
		}
	}

	// (3) Refresh path: ask the AuthClient for a valid token. GetToken
	// checks the stored credential, sees it's expiring soon, and calls
	// refreshTokenLocked automatically. Returns empty if the store is
	// empty (first run) or the refresh fails — we treat either case as
	// "fall through to browser login" below.
	//
	// Skipped if the stored credential no longer covers the requested
	// scope set — a refresh exchange with the same scopes would not
	// help, so we re-run the full flow to widen the grant.
	if s.canTryRefresh() {
		if tok, err := s.oaClient.GetToken(); err == nil && tok != "" {
			cred, _ := s.oaClient.GetCredential()
			if cred != nil && !cred.IsExpired() && credentialCoversScopes(cred, scopes) {
				s.token = tok
				s.expiry = cred.ExpiresAt
				return tok, nil
			}
		}
	}

	// (4) Full browser login flow with explicit endpoints from discovery.
	// Pass TokenEndpointAuthMethods from AS metadata so auth method negotiation
	// works correctly even with explicit endpoints (oneauth#74).
	// oneauth 0.1.9 (#217): BrowserLoginConfig renamed to BrowserLoginRequest;
	// LoginWithBrowser now takes (ctx, *BrowserLoginRequest).
	expectedIssuer := s.authInfo.AuthorizationServers[0]
	// SEP-2468 / RFC 9207 §2.4: validate the iss query parameter on the
	// OAuth callback before token exchange. oneauth surfaces the value
	// via OnCallback (#235) but explicitly leaves enforcement policy to
	// the consumer. mcpkit's policy:
	//   iss present → MUST match expected issuer (byte-for-byte; §2.4
	//                 forbids normalization)
	//   iss absent + AS advertised → MUST reject (the advertisement
	//                                 promise binds the AS)
	//   iss absent + AS did not advertise → accept (legacy AS pre-9207)
	//
	// asAdvertisedSupport is read from the AS metadata field surfaced
	// by oneauth v0.1.12 (oneauth#239). nil means the AS metadata didn't
	// include the field at all → treat as legacy.
	asAdvertisedSupport := false
	if flag := s.authInfo.ASMetadata.AuthorizationResponseIssParameterSupported; flag != nil && *flag {
		asAdvertisedSupport = true
	}
	loginReq := &client.BrowserLoginRequest{
		AuthorizationEndpoint:    s.authInfo.ASMetadata.AuthorizationEndpoint,
		TokenEndpoint:            s.authInfo.ASMetadata.TokenEndpoint,
		TokenEndpointAuthMethods: s.authInfo.ASMetadata.TokenEndpointAuthMethods,
		ClientID:                 clientID,
		ClientSecret:             clientSecret,
		Scopes:                   scopes,
		Resource:                 s.ServerURL, // RFC 8707 §2: send the resource parameter to bind the token to this MCP server
		OpenBrowser:              s.OpenBrowser,
		OnCallback: func(_ context.Context, p client.CallbackParams) error {
			return validateIss(p.Iss, expectedIssuer, asAdvertisedSupport)
		},
	}
	if s.HTTPClient != nil {
		loginReq.HTTPClient = s.HTTPClient
	}
	// ClientSecret is passed in BrowserLoginRequest above — oneauth's
	// SelectAuthMethod handles negotiation (basic/post/none) based on AS metadata.

	cred, err := s.oaClient.LoginWithBrowser(context.Background(), loginReq)
	if err != nil {
		return "", fmt.Errorf("oauth login: %w", err)
	}

	s.token = cred.AccessToken
	s.expiry = cred.ExpiresAt
	return s.token, nil
}

// credStore returns the CredentialStore the underlying AuthClient should
// use. Prefers the caller-supplied s.CredStore for external persistence;
// falls back to an internal MemoryCredentialStore so the refresh path has
// somewhere to read the current credential from even when the caller
// doesn't care about cross-process persistence.
//
// Called only while s.mu is held. The internal memory store is allocated
// once and cached on s.memoryStore.
func (s *OAuthTokenSource) credStore() client.CredentialStore {
	if s.CredStore != nil {
		return s.CredStore
	}
	if s.memoryStore == nil {
		s.memoryStore = NewMemoryCredentialStore()
	}
	return s.memoryStore
}

// canTryRefresh reports whether the refresh path has a chance of
// returning a usable token. Called only while s.mu is held.
func (s *OAuthTokenSource) canTryRefresh() bool {
	if s.oaClient == nil {
		return false
	}
	cred, err := s.oaClient.GetCredential()
	if err != nil || cred == nil {
		return false
	}
	if !cred.HasRefreshToken() {
		return false
	}
	return credentialCoversScopes(cred, s.Scopes)
}

// credentialCoversScopes reports whether the stored credential's scope
// set is a superset of the requested scopes. A credential issued for
// [read] cannot be refreshed into [read, write] via the refresh_token
// grant on most AS implementations — we have to re-run the full flow to
// widen the grant. Returns true when required is empty (nothing to
// cover).
//
// Scope comparison is space-separated per RFC 6749 §3.3.
func credentialCoversScopes(cred *client.ServerCredential, required []string) bool {
	if cred == nil {
		return false
	}
	if len(required) == 0 {
		return true
	}
	have := make(map[string]struct{})
	for _, s := range strings.Fields(cred.Scope) {
		have[s] = struct{}{}
	}
	for _, s := range required {
		if _, ok := have[s]; !ok {
			return false
		}
	}
	return true
}

// TokenForScopes implements core.ScopeAwareTokenSource.
// Invalidates the cached token and the stored credential (if any), merges
// the requested scopes into the existing scope set, and triggers a fresh
// OAuth flow. Stored-credential invalidation is required so the refresh
// path cannot silently return a token scoped to the old grant — step-up
// must re-run LoginWithBrowser to widen the scope set.
//
// Calling this also arms the source for acquisition (see Token's lazy
// gate). The transport routes every 401 on a scope-aware source through
// here — including 401s whose WWW-Authenticate carries no scope=, where
// scopes is empty. That empty call is a no-op on the scope set but still
// arms the source, so the subsequent Token acquires using the PRM
// scopes_supported fallback rather than deferring with core.ErrNoTokenYet.
func (s *OAuthTokenSource) TokenForScopes(scopes []string) (string, error) {
	s.mu.Lock()
	s.token = ""
	s.expiry = time.Time{}
	// Arm acquisition: a server challenge (or explicit step-up) has selected
	// the scope, so the next Token call must acquire rather than defer with
	// core.ErrNoTokenYet. Holds even when scopes is empty — an empty union
	// is a no-op on the scope set but still flips the source out of its lazy
	// state, so a 401 carrying no scope= falls through to the PRM catalog.
	s.armed = true
	s.Scopes = core.UnionScopes(s.Scopes, scopes)
	// Wipe the stored credential so Token() skips the refresh path and
	// goes straight to LoginWithBrowser on the next call. Applies to
	// both user-provided CredStore and the internal memoryStore — both
	// are aliased by s.credStore() and managed through the same
	// AuthClient. Errors are logged-and-swallowed because the fallback
	// (full re-login) is still correct; we only care that the refresh
	// path doesn't short-circuit.
	if store := s.credStore(); store != nil {
		_ = store.RemoveCredential(s.ServerURL)
	}
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
//
// SEP-2352 cross-AS credential-reuse prevention: before consulting the
// cached DCR credentials, compare the AS they were issued against
// (dcrAS) with the current AS (from authInfo). On mismatch — i.e., the
// server's PRM has switched authorization_servers since the cache was
// populated — drop the cache so the DCR branch below re-registers at
// the new AS. Without this, the client would silently present AS₁'s
// client_id to AS₂, which the upstream `sep-2352-no-reuse-on-as-change`
// check rejects as a security violation.
// MCP-Auth §C6 — client registration priority: a pre-registered ClientID
// wins over a CIMD ClientMetadataURL, which wins over Dynamic Client
// Registration. resolveClientID walks the three in that fixed order.
func (s *OAuthTokenSource) resolveClientID() (clientID, clientSecret string, err error) {
	// 1. Pre-registered
	if s.ClientID != "" {
		return s.ClientID, s.ClientSecret, nil
	}

	// 2. CIMD (C7/C8) — preferred exactly when the AS advertises
	// client_id_metadata_document_supported (SEP-991 SHOULD). When the AS
	// does not advertise it and DCR is available, fall through to DCR so a
	// generic client configured with both works against either kind of AS.
	// When there is no DCR fallback, still present the URL best-effort:
	// a CIMD-only configuration must keep working against an AS that
	// supports CIMD without advertising it.
	if s.ClientMetadataURL != "" {
		if err := client.ValidateCIMDURL(s.ClientMetadataURL); err != nil {
			// Log warning but fall through to DCR
			_ = err
		} else {
			advertised := s.authInfo != nil && s.authInfo.ASMetadata != nil &&
				s.authInfo.ASMetadata.ClientIdMetadataDocumentSupported
			dcrAvailable := s.EnableDCR && s.authInfo != nil && s.authInfo.ASMetadata != nil &&
				s.authInfo.ASMetadata.RegistrationEndpoint != ""
			if advertised || !dcrAvailable {
				return s.ClientMetadataURL, "", nil
			}
		}
	}

	// SEP-2352: clear cached DCR credentials when the AS has changed
	// since they were issued. currentAS may be empty when authInfo is
	// not yet populated; that's harmless (the cache is also empty in
	// that case).
	currentAS := ""
	if s.authInfo != nil && len(s.authInfo.AuthorizationServers) > 0 {
		currentAS = s.authInfo.AuthorizationServers[0]
	}
	if s.dcrClientID != "" && s.dcrAS != "" && s.dcrAS != currentAS {
		s.dcrClientID = ""
		s.dcrClientSecret = ""
		s.dcrAS = ""
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
		s.dcrAS = currentAS
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
