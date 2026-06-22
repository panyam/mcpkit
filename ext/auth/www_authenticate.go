package auth

import (
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// WWWAuth401 builds a WWW-Authenticate header value for 401 Unauthorized responses.
// Per MCP spec (2025-11-25): includes resource_metadata URL and optional scopes.
//
// Example output:
//
//	Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="tools:read admin:write"
func WWWAuth401(resourceMetadataURL string, scopes ...string) string {
	parts := []string{fmt.Sprintf(`resource_metadata="%s"`, resourceMetadataURL)}
	if len(scopes) > 0 {
		parts = append(parts, fmt.Sprintf(`scope="%s"`, strings.Join(scopes, " ")))
	}
	return "Bearer " + strings.Join(parts, ", ")
}

// WWWAuth403 builds a WWW-Authenticate header value for 403 Forbidden responses
// with insufficient scope. Per MCP spec (2025-11-25) and RFC 6750 §3.1, includes
// the `error="insufficient_scope"` token plus the required scopes, and optionally
// the `resource_metadata="<URL>"` link to the server's RFC 9728 PRM document so
// callers don't have to fall back to the well-known path to discover it.
//
// resourceMetadataURL empty omits the `resource_metadata` segment entirely. This
// is symmetric with WWWAuth401, which always emits the link, and matches the
// shape modelcontextprotocol/typescript-sdk PR 1624 (the SEP-2350 server-side
// reference impl) emits on its own scope-challenge 403s — see RFC 9728 §5.1
// for the discovery semantics this enables.
//
// Stateless-wire 403s are the load-bearing case: there is no preceding 401 to
// learn the PRM link from, so the very first request that lands a 403 needs
// the link inline to discover the AS.
//
// Example output:
//
//	Bearer error="insufficient_scope", resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="admin:write files:read"
func WWWAuth403(resourceMetadataURL string, scopes ...string) string {
	parts := []string{`error="insufficient_scope"`}
	if resourceMetadataURL != "" {
		parts = append(parts, fmt.Sprintf(`resource_metadata="%s"`, resourceMetadataURL))
	}
	if len(scopes) > 0 {
		parts = append(parts, fmt.Sprintf(`scope="%s"`, strings.Join(scopes, " ")))
	}
	return "Bearer " + strings.Join(parts, ", ")
}

// ParseWWWAuthenticate extracts the resource_metadata URL and scopes from a
// WWW-Authenticate: Bearer header value. Used by MCP clients to discover
// the PRM endpoint after receiving a 401.
//
// Per spec: clients MUST use resource_metadata from WWW-Authenticate when present.
//
// Delegates to core.ParseWWWAuthenticate (core module) — the parser lives in
// core so the client transport can use it without depending on the auth sub-module.
func ParseWWWAuthenticate(header string) (resourceMetadata string, scopes []string, err error) {
	return core.ParseWWWAuthenticate(header)
}
