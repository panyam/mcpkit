package auth

import (
	"fmt"
	"strings"
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
func ParseWWWAuthenticate(header string) (resourceMetadata string, scopes []string, err error) {
	// Strip "Bearer " prefix
	header = strings.TrimSpace(header)
	if strings.HasPrefix(header, "Bearer ") {
		header = header[len("Bearer "):]
	}

	resourceMetadata = extractParam(header, "resource_metadata")
	scopeStr := extractParam(header, "scope")
	if scopeStr != "" {
		scopes = strings.Fields(scopeStr)
	}

	return resourceMetadata, scopes, nil
}

// extractParam extracts a named parameter value from a WWW-Authenticate header.
// Handles both quoted ("value") and unquoted (value) parameter formats.
func extractParam(header, name string) string {
	// Look for name="value" or name=value
	search := name + "="
	idx := strings.Index(header, search)
	if idx < 0 {
		return ""
	}
	rest := header[idx+len(search):]

	if len(rest) > 0 && rest[0] == '"' {
		// Quoted value — find closing quote
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return rest[1:] // unclosed quote, return rest
		}
		return rest[1 : end+1]
	}

	// Unquoted value — delimited by comma or space
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
