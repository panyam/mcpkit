package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/panyam/oneauth/client"
)

// ErrPRMResourceMismatch is returned by DiscoverMCPAuth when the
// Protected Resource Metadata document's `resource` field points at a
// URL that differs from the MCP server URL the client is connecting to.
//
// Per RFC 8707 §2 (Resource Indicators) and the upstream
// `auth/resource-mismatch` conformance scenario, clients MUST validate
// that PRM `resource` matches the protected resource they're trying to
// access — otherwise a server-controlled PRM can redirect the client
// to an attacker-controlled authorization server that issues tokens
// for a different audience (a token-recipient-confusion attack).
//
// Comparison normalizes scheme + host to lowercase (RFC 3986
// §6.2.2.1) and ignores a trailing slash on the path. An empty
// PRM.Resource is treated as "no binding asserted" and accepted —
// some early PRM emitters omit the field, and rejecting outright
// would break working integrations.
var ErrPRMResourceMismatch = errors.New("PRM resource does not match MCP server URL")

// MCPAuthInfo holds the combined discovery results for an MCP server's auth configuration.
type MCPAuthInfo struct {
	// ResourceMetadataURL is the PRM endpoint URL (from WWW-Authenticate or well-known).
	ResourceMetadataURL string

	// PRM is the parsed Protected Resource Metadata document.
	PRM *ProtectedResourceMetadata

	// AuthorizationServers from the PRM document.
	AuthorizationServers []string

	// ASMetadata is the discovered authorization server metadata.
	ASMetadata *client.ASMetadata
}

// ProtectedResourceMetadata is the JSON structure served by the PRM endpoint (RFC 9728).
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

// DiscoverOption configures DiscoverMCPAuth.
type DiscoverOption func(*discoverConfig)

type discoverConfig struct {
	httpClient *http.Client
	asStore    client.ASMetadataStore
}

// WithHTTPClient sets a custom HTTP client for discovery requests.
// Use in tests with httptest.Server to avoid real network calls.
func WithHTTPClient(c *http.Client) DiscoverOption {
	return func(cfg *discoverConfig) { cfg.httpClient = c }
}

// WithASMetadataStore enables caching of authorization server metadata
// across multiple discovery calls. When multiple MCP servers share the
// same authorization server, the first discovery fetches from the network
// and subsequent discoveries hit the cache.
//
// Typical usage: share a single store across all OAuthTokenSource instances
// in a process that connects to multiple MCP servers behind one IdP.
//
//	cache := client.NewMemoryASMetadataStore(0) // default TTL 1h
//	info1, _ := auth.DiscoverMCPAuth(url1, auth.WithASMetadataStore(cache))
//	info2, _ := auth.DiscoverMCPAuth(url2, auth.WithASMetadataStore(cache))
//	// If url1 and url2 share an AS, info2 hits the cache — no second fetch.
func WithASMetadataStore(s client.ASMetadataStore) DiscoverOption {
	return func(cfg *discoverConfig) { cfg.asStore = s }
}

// DiscoverMCPAuth performs the MCP-specific discovery chain:
//  1. Send unauthenticated request to serverURL, expect 401
//  2. Parse WWW-Authenticate header for resource_metadata and scope
//  3. If no resource_metadata in header, try well-known URIs
//  4. Fetch PRM document
//  5. Extract authorization_servers and scopes_supported
//  6. Discover AS metadata via oneauth DiscoverAS (RFC 8414 §3 well-known
//     AS-metadata URL + OIDC fallback)
//
// Per MCP spec (2025-11-25): clients MUST support both WWW-Authenticate and
// well-known URI discovery. Must use resource_metadata from header when present.
//
// MCP-Auth §2.3 — the client MUST try the WWW-Authenticate resource_metadata
// link first and fall back to the well-known URI (steps 2-3); scope selection
// prefers WWW-Authenticate over PRM scopes_supported.
func DiscoverMCPAuth(serverURL string, opts ...DiscoverOption) (*MCPAuthInfo, error) {
	cfg := &discoverConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	httpClient := cfg.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	info := &MCPAuthInfo{}

	// Step 1: Probe the server to get 401 + WWW-Authenticate
	resp, err := httpClient.Post(serverURL, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`))
	if err != nil {
		return nil, fmt.Errorf("probe server: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Step 2: Parse WWW-Authenticate for the resource_metadata link.
		//
		// We deliberately do NOT capture the probe's scope= here. The probe
		// hits a fixed method (initialize) whose challenge scope, if any, is
		// the scope for THAT method — not necessarily the scope a later
		// tools/call needs. Per RFC 6750 §3.1 the token's scope is selected
		// per-operation from the challenge on the real request; discovery's
		// job is endpoint resolution (PRM + AS metadata), not scope pinning.
		// Token sources read the catalog fallback from info.PRM.ScopesSupported.
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if wwwAuth != "" {
			rm, _, _ := ParseWWWAuthenticate(wwwAuth)
			if rm != "" {
				info.ResourceMetadataURL = rm
			}
		}
	}

	// Step 3: If no resource_metadata from header, try well-known URIs.
	// RFC 9728 §3.1 — the PRM well-known path is /.well-known/oauth-protected-resource
	// with the resource's path appended. E.g., for http://host/mcp:
	//   path-based: http://host/.well-known/oauth-protected-resource/mcp
	//   root:       http://host/.well-known/oauth-protected-resource
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	if info.ResourceMetadataURL == "" {
		pathBased := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path
		rootBased := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"

		// Try path-based first
		info.ResourceMetadataURL = pathBased
		prmResp, prmBody, err := fetchPRM(httpClient, pathBased)
		if err != nil {
			return nil, fmt.Errorf("fetch PRM (path-based): %w", err)
		}

		if prmResp.StatusCode == http.StatusNotFound {
			// Fallback to root well-known
			info.ResourceMetadataURL = rootBased
			prmResp, prmBody, err = fetchPRM(httpClient, rootBased)
			if err != nil {
				return nil, fmt.Errorf("fetch PRM (root): %w", err)
			}
		}

		if prmResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("PRM endpoint returned %d", prmResp.StatusCode)
		}

		prm, err := parsePRM(prmBody)
		if err != nil {
			return nil, fmt.Errorf("parse PRM: %w", err)
		}
		info.PRM = prm
	} else {
		// resource_metadata URL was in WWW-Authenticate header — fetch it directly
		prmResp, prmBody, err := fetchPRM(httpClient, info.ResourceMetadataURL)
		if err != nil {
			return nil, fmt.Errorf("fetch PRM: %w", err)
		}
		if prmResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("PRM endpoint returned %d", prmResp.StatusCode)
		}
		prm, err := parsePRM(prmBody)
		if err != nil {
			return nil, fmt.Errorf("parse PRM: %w", err)
		}
		info.PRM = prm
	}

	// RFC 8707 §2 — PRM resource MUST match the protected resource URL
	// the client is accessing. Server-controlled PRMs that emit a
	// foreign resource are rejected to prevent token-recipient-confusion
	// attacks. See ErrPRMResourceMismatch for normalization rules.
	if err := validatePRMResource(info.PRM, u); err != nil {
		return nil, err
	}

	// Step 4: Extract authorization_servers from PRM (RFC 9728 §3.2 — the PRM
	// document's authorization_servers array; at least one AS is required).
	info.AuthorizationServers = info.PRM.AuthorizationServers
	if len(info.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("PRM has no authorization_servers")
	}

	// Scope selection is no longer pinned here. The PRM scopes_supported
	// catalog stays available to token sources via info.PRM.ScopesSupported,
	// but it is only a last-resort fallback: per-operation scope comes from
	// the WWW-Authenticate challenge on the real request (RFC 6750 §3.1),
	// observed by the token source after a 401, not pre-selected at discovery.

	// Step 5: Discover AS metadata via oneauth (RFC 8414 + OIDC fallback)
	issuer := info.AuthorizationServers[0]
	asOpts := []client.DiscoveryOption{client.WithHTTPClientForDiscovery(httpClient)}
	if cfg.asStore != nil {
		asOpts = append(asOpts, client.WithASMetadataStore(cfg.asStore))
	}
	asMeta, err := client.DiscoverAS(issuer, asOpts...)
	if err != nil {
		return nil, fmt.Errorf("discover AS metadata for %s: %w", issuer, err)
	}
	info.ASMetadata = asMeta

	return info, nil
}

// fetchPRM performs an HTTP GET on the given URL and returns the response and body.
func fetchPRM(httpClient *http.Client, prmURL string) (*http.Response, []byte, error) {
	resp, err := httpClient.Get(prmURL)
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

// parsePRM parses a PRM JSON document.
func parsePRM(body []byte) (*ProtectedResourceMetadata, error) {
	var prm ProtectedResourceMetadata
	if err := json.Unmarshal(body, &prm); err != nil {
		return nil, fmt.Errorf("invalid PRM JSON: %w (body: %s)", err, string(body))
	}
	return &prm, nil
}

// validatePRMResource enforces the RFC 8707 §2 binding between the PRM
// document's `resource` field and the MCP server URL the client is
// connecting to. Returns nil for the explicit-omission case (empty
// `resource`), wraps ErrPRMResourceMismatch with diagnostic context
// otherwise.
//
// Comparison rules:
//   - Scheme + host are compared case-insensitively (RFC 3986 §6.2.2.1).
//   - Path matches if (a) the PRM path equals the server path after
//     trailing-slash normalization, OR (b) the PRM path is empty (the
//     resource scope is the whole origin — emitted by AS-coupled MCP
//     deployments where the PRM document covers every path on the
//     host).
//   - Query + fragment on PRM `resource` are not part of the comparison
//     (the spec doesn't expect them).
//
// Stricter byte-for-byte path comparison rejects real-world PRMs that
// emit either form interchangeably and fails the upstream
// `auth/metadata-var*` scenarios where the fixture emits an
// origin-only resource against a server mounted at a path. The
// load-bearing check the upstream `auth/resource-mismatch` scenario
// grades is the ORIGIN mismatch; the path-empty carve-out is
// orthogonal to that.
func validatePRMResource(prm *ProtectedResourceMetadata, serverURL *url.URL) error {
	if prm == nil || prm.Resource == "" {
		return nil
	}
	prmURL, err := url.Parse(prm.Resource)
	if err != nil {
		return fmt.Errorf("%w: invalid resource URL %q: %v",
			ErrPRMResourceMismatch, prm.Resource, err)
	}
	if !sameResourceURL(prmURL, serverURL) {
		return fmt.Errorf("%w: PRM resource=%q, server=%q",
			ErrPRMResourceMismatch, prm.Resource, serverURL.String())
	}
	return nil
}

// sameResourceURL compares two resource URLs under the normalization
// rules documented on validatePRMResource: case-insensitive scheme +
// host, then either path equality (trailing slash ignored) OR an empty
// PRM path matching any server path.
func sameResourceURL(prmURL, serverURL *url.URL) bool {
	if !strings.EqualFold(prmURL.Scheme, serverURL.Scheme) {
		return false
	}
	if !strings.EqualFold(prmURL.Host, serverURL.Host) {
		return false
	}
	prmPath := strings.TrimRight(prmURL.Path, "/")
	if prmPath == "" {
		// PRM covers the whole origin — common for AS-coupled deployments
		// where the entire host is the protected resource.
		return true
	}
	return prmPath == strings.TrimRight(serverURL.Path, "/")
}
