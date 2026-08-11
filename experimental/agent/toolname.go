package agent

import (
	"regexp"
	"strconv"

	"github.com/panyam/mcpkit/core"
)

// Both OpenAI and Anthropic constrain a function/tool name to
// ^[a-zA-Z0-9_-]{1,64}$. A connected MCP server may expose a tool whose name
// violates it — a dot ("weather.current"), a slash, spaces, or more than 64
// characters — and the provider then rejects the whole request with an HTTP 400
// that aborts the turn. The provider sends a sanitized name on the wire and maps
// the model's response back to the real name so dispatch still resolves.
var invalidToolNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

const maxToolNameLen = 64

// sanitizeToolName maps an arbitrary tool name to one matching the provider
// constraint ^[a-zA-Z0-9_-]{1,64}$: runs of invalid characters collapse to a
// single "_", an empty result becomes "tool", and the result is truncated to 64
// runes. It is deterministic per name and a no-op on an already-valid name;
// cross-tool uniqueness is toolNameMaps' job.
func sanitizeToolName(name string) string {
	s := invalidToolNameChars.ReplaceAllString(name, "_")
	if s == "" {
		s = "tool"
	}
	return truncateToolName(s)
}

func truncateToolName(s string) string {
	if len(s) > maxToolNameLen {
		return s[:maxToolNameLen]
	}
	return s
}

// toolNameMaps builds a request's bijection between real tool names and the
// provider-safe names actually put on the wire. It is deterministic in tools
// order, so the request builder (real→safe) and the response parser (safe→real)
// each derive the same maps from req.Tools with no shared state to thread. Two
// names that sanitize to the same string are disambiguated with a numeric
// suffix, so the mapping stays reversible.
func toolNameMaps(tools []core.ToolDef) (realToSafe, safeToReal map[string]string) {
	realToSafe = make(map[string]string, len(tools))
	safeToReal = make(map[string]string, len(tools))
	for _, td := range tools {
		if _, seen := realToSafe[td.Name]; seen {
			continue // duplicate real name: first mapping wins
		}
		safe := sanitizeToolName(td.Name)
		for n := 2; ; n++ {
			if _, taken := safeToReal[safe]; !taken {
				break
			}
			suffix := "_" + strconv.Itoa(n)
			base := sanitizeToolName(td.Name)
			if len(base)+len(suffix) > maxToolNameLen {
				base = base[:maxToolNameLen-len(suffix)]
			}
			safe = base + suffix
		}
		realToSafe[td.Name] = safe
		safeToReal[safe] = td.Name
	}
	return
}

// safeToolName returns the wire name for a real tool name using the request map,
// falling back to a direct sanitize for a name not in the current tool set (an
// assistant history tool_call for a tool no longer offered) — the fallback keeps
// the request valid even though that name will not be reversed.
func safeToolName(realToSafe map[string]string, name string) string {
	if s, ok := realToSafe[name]; ok {
		return s
	}
	return sanitizeToolName(name)
}

// realToolName reverses a wire name from a model response back to the real tool
// name, leaving unknown names (already-valid names map to themselves) untouched.
func realToolName(safeToReal map[string]string, name string) string {
	if r, ok := safeToReal[name]; ok {
		return r
	}
	return name
}
