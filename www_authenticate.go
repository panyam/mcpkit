package mcpkit

// WWW-Authenticate header parsing for MCP clients.
//
// These functions live in the core module (not auth/) because the client
// transport needs them for 401/403 handling, and core must not depend on
// the auth sub-module. They are pure string parsing with zero external deps.
//
// The auth sub-module's ParseWWWAuthenticate delegates to these functions
// for backward compatibility.

import "strings"

// ParseWWWAuthenticate extracts the resource_metadata URL and scopes from a
// WWW-Authenticate: Bearer header value. Used by MCP clients to discover
// the PRM endpoint after receiving a 401, and to parse required scopes from
// a 403 insufficient_scope response.
//
// Per MCP spec (2025-11-25): clients MUST use resource_metadata from
// WWW-Authenticate when present.
func ParseWWWAuthenticate(header string) (resourceMetadata string, scopes []string, err error) {
	header = strings.TrimSpace(header)
	if strings.HasPrefix(header, "Bearer ") {
		header = header[len("Bearer "):]
	}

	resourceMetadata = extractWWWAuthParam(header, "resource_metadata")
	scopeStr := extractWWWAuthParam(header, "scope")
	if scopeStr != "" {
		scopes = strings.Fields(scopeStr)
	}

	return resourceMetadata, scopes, nil
}

// extractWWWAuthParam extracts a named parameter value from a WWW-Authenticate
// header. Handles both quoted ("value") and unquoted (value) parameter formats.
// Ensures the match is a full parameter name (not a suffix like "noscope" when
// searching for "scope").
func extractWWWAuthParam(header, name string) string {
	search := name + "="
	idx := strings.Index(header, search)
	for idx >= 0 {
		if idx == 0 || header[idx-1] == ' ' || header[idx-1] == ',' {
			break
		}
		next := strings.Index(header[idx+1:], search)
		if next < 0 {
			return ""
		}
		idx = idx + 1 + next
	}
	if idx < 0 {
		return ""
	}
	rest := header[idx+len(search):]

	if len(rest) > 0 && rest[0] == '"' {
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return rest[1:]
		}
		return rest[1 : end+1]
	}

	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
