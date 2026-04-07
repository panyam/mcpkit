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
// with insufficient scope. Per MCP spec (2025-11-25), includes error code and
// required scopes.
//
// Example output:
//
//	Bearer error="insufficient_scope", scope="admin:write files:read"
func WWWAuth403(scopes ...string) string {
	parts := []string{`error="insufficient_scope"`}
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
