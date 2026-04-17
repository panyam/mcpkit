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

	// Validator, if set, is automatically wired with ScopesSupported as its
	// AllScopes field (if not already populated). This ensures that 401
	// WWW-Authenticate responses include the full scope list, allowing clients
	// to request broad scopes upfront and avoid scope step-up round-trips.
	//
	// Without this wiring, Validator.AllScopes defaults to empty and 401
	// responses omit the scope= parameter, forcing clients through a
	// narrow-to-broad scope dance. See #50.
	Validator *JWTValidator
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
//	mux.Handle("/mcp", srv.Handler(core.WithStreamableHTTP(true)))
//	auth.MountAuth(mux, auth.AuthConfig{
//	    ResourceURI:          "https://mcp.example.com",
//	    AuthorizationServers: []string{"https://auth.example.com"},
//	    ScopesSupported:      []string{"tools:read", "tools:call", "admin:write"},
//	    MCPPath:              "/mcp",
//	})
func MountAuth(mux *http.ServeMux, cfg AuthConfig) {
	// Auto-wire AllScopes from ScopesSupported for the provided validator.
	// This ensures 401 responses include all scopes the server supports,
	// reducing scope step-up round-trips for LLM clients.
	if cfg.Validator != nil && len(cfg.Validator.AllScopes) == 0 {
		cfg.Validator.AllScopes = cfg.ScopesSupported
	}

	meta := &apiauth.ProtectedResourceMetadata{
		Resource:             cfg.ResourceURI,
		AuthorizationServers: cfg.AuthorizationServers,
		ScopesSupported:      cfg.ScopesSupported,
	}

	// Mount PRM + RFC 8414 AS metadata proxy. The proxy ensures clients
	// that only try RFC 8414 (not OIDC fallback) can discover AS endpoints.
	// See: https://github.com/panyam/oneauth/issues/86
	apiauth.MountProtectedResource(mux, meta, true, cfg.MCPPath)
}
