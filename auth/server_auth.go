package auth

import (
	"net/http"

	"github.com/panyam/oneauth/apiauth"
)

// AuthConfig configures the OAuth well-known endpoints for an MCP server.
type AuthConfig struct {
	// ResourceURI is the canonical URI of this MCP server (RFC 8707 resource indicator).
	ResourceURI string

	// AuthorizationServers lists the authorization server URLs this server trusts.
	// At least one is required per RFC 9728.
	AuthorizationServers []string

	// ScopesSupported lists the OAuth scopes this server understands.
	ScopesSupported []string

	// MCPPath is the MCP endpoint path (e.g., "/mcp"). Used to construct
	// the well-known URI: /.well-known/oauth-protected-resource/<mcpPath>
	MCPPath string
}

// MountAuth registers the OAuth Protected Resource Metadata endpoint (RFC 9728)
// on the given mux. This enables MCP clients to discover the authorization server
// via the well-known URI.
//
// Per MCP spec (2025-11-25): MCP servers MUST implement RFC 9728 PRM.
//
// Usage:
//
//	mux := http.NewServeMux()
//	mux.Handle("/mcp", srv.Handler(mcpkit.WithStreamableHTTP(true)))
//	auth.MountAuth(mux, auth.AuthConfig{
//	    ResourceURI:          "https://mcp.example.com",
//	    AuthorizationServers: []string{"https://auth.example.com"},
//	    ScopesSupported:      []string{"tools:read", "tools:call", "admin:write"},
//	    MCPPath:              "/mcp",
//	})
func MountAuth(mux *http.ServeMux, cfg AuthConfig) {
	meta := &apiauth.ProtectedResourceMetadata{
		Resource:             cfg.ResourceURI,
		AuthorizationServers: cfg.AuthorizationServers,
		ScopesSupported:      cfg.ScopesSupported,
	}

	handler := apiauth.NewProtectedResourceHandler(meta)

	// Mount at path-based well-known URI (primary)
	if cfg.MCPPath != "" {
		mux.Handle("/.well-known/oauth-protected-resource"+cfg.MCPPath, handler)
	}
	// Also mount at root well-known URI (fallback)
	mux.Handle("/.well-known/oauth-protected-resource", handler)
}
