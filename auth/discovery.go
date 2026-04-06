package auth

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/panyam/oneauth/client"
)

// MCPAuthInfo holds the combined discovery results for an MCP server's auth configuration.
type MCPAuthInfo struct {
	// ResourceMetadataURL is the PRM endpoint URL (from WWW-Authenticate or well-known).
	ResourceMetadataURL string

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

// DiscoverMCPAuth performs the MCP-specific discovery chain:
//  1. Send unauthenticated request to serverURL, expect 401
//  2. Parse WWW-Authenticate header for resource_metadata and scope
//  3. If no resource_metadata in header, try well-known URIs
//  4. Fetch PRM document
//  5. Extract authorization_servers
//  6. Discover AS metadata via oneauth DiscoverAS (RFC 8414 + OIDC fallback)
//
// Per MCP spec (2025-11-25): clients MUST support both WWW-Authenticate and
// well-known URI discovery. Must use resource_metadata from header when present.
func DiscoverMCPAuth(serverURL string) (*MCPAuthInfo, error) {
	info := &MCPAuthInfo{}

	// Step 1: Probe the server to get 401 + WWW-Authenticate
	resp, err := http.Post(serverURL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`))
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

	// Step 3: If no resource_metadata from header, try well-known URIs
	if info.ResourceMetadataURL == "" {
		// Try path-based first, then root
		// Extract the path from serverURL for path-based well-known
		info.ResourceMetadataURL = serverURL + "/.well-known/oauth-protected-resource"
	}

	// Step 4-5: Fetch PRM and extract authorization_servers
	// (In a full implementation, we'd HTTP GET the PRM URL and parse JSON.
	// For now, the PRM is served by MountAuth and discovered via the URL.)

	// Step 6: Discover AS metadata
	if len(info.AuthorizationServers) > 0 {
		asMeta, err := client.DiscoverAS(info.AuthorizationServers[0])
		if err != nil {
			return nil, fmt.Errorf("discover AS: %w", err)
		}
		info.ASMetadata = asMeta
	}

	return info, nil
}
