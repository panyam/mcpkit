package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/panyam/oneauth/client"
)

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

	// Scopes to request, determined via the MCP scope selection strategy:
	// 1. scope from WWW-Authenticate header
	// 2. scopes_supported from PRM
	// 3. empty (omit scope parameter)
	Scopes []string
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
}

// WithHTTPClient sets a custom HTTP client for discovery requests.
// Use in tests with httptest.Server to avoid real network calls.
func WithHTTPClient(c *http.Client) DiscoverOption {
	return func(cfg *discoverConfig) { cfg.httpClient = c }
}

// DiscoverMCPAuth performs the MCP-specific discovery chain:
//  1. Send unauthenticated request to serverURL, expect 401
//  2. Parse WWW-Authenticate header for resource_metadata and scope
//  3. If no resource_metadata in header, try well-known URIs
//  4. Fetch PRM document
//  5. Extract authorization_servers and scopes_supported
//  6. Discover AS metadata via oneauth DiscoverAS (RFC 8414 + OIDC fallback)
//
// Per MCP spec (2025-11-25): clients MUST support both WWW-Authenticate and
// well-known URI discovery. Must use resource_metadata from header when present.
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
		// Step 2: Parse WWW-Authenticate
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if wwwAuth != "" {
			rm, scopes, _ := ParseWWWAuthenticate(wwwAuth)
			if rm != "" {
				info.ResourceMetadataURL = rm
			}
			if len(scopes) > 0 {
				info.Scopes = scopes
			}
		}
	}

	// Step 3: If no resource_metadata from header, try well-known URIs.
	// Per RFC 9728: the well-known path is /.well-known/oauth-protected-resource
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

	// Step 4: Extract authorization_servers from PRM
	info.AuthorizationServers = info.PRM.AuthorizationServers
	if len(info.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("PRM has no authorization_servers")
	}

	// Step 5: Scope selection (C18)
	// Priority: WWW-Authenticate scope (already set) > PRM scopes_supported > empty
	if len(info.Scopes) == 0 && len(info.PRM.ScopesSupported) > 0 {
		info.Scopes = info.PRM.ScopesSupported
	}

	// Step 6: Discover AS metadata via oneauth (RFC 8414 + OIDC fallback)
	issuer := info.AuthorizationServers[0]
	asMeta, err := client.DiscoverAS(issuer, client.WithHTTPClientForDiscovery(httpClient))
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
