package client

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// SEP-2243 standard routing headers. Mirrors the JSON-RPC method (and, for
// tools/call / prompts/get / resources/read, the params.name or params.uri)
// onto HTTP headers so proxies and middleware can route requests without
// parsing the JSON body. See server/header_validation.go for the matching
// server-side check.
const (
	mcpMethodHeader = "Mcp-Method"
	mcpNameHeader   = "Mcp-Name"
)

// setSEP2243RoutingHeaders attaches the SEP-2243 standard routing headers to a
// streamable HTTP request. method is the JSON-RPC method carried in the body
// and is always mirrored onto Mcp-Method; cc may carry a per-call mcpName
// (derived centrally in rawCallWithContext for tools/call / prompts/get /
// resources/read) that is mirrored onto Mcp-Name. Passing nil cc, or a cc
// without mcpName set, simply skips Mcp-Name — Mcp-Method is unconditional.
func setSEP2243RoutingHeaders(req *http.Request, method string, cc *CallContext) {
	if method != "" {
		req.Header.Set(mcpMethodHeader, method)
	}
	if cc != nil && cc.mcpName != "" {
		req.Header.Set(mcpNameHeader, cc.mcpName)
	}
}

// deriveMcpName extracts the SEP-2243 Mcp-Name value from a JSON-RPC params
// payload for the three methods that carry a routable name/URI in their
// params: tools/call (params.name), prompts/get (params.name), and
// resources/read (params.uri). Returns "" for any other method or when the
// field is missing / non-string — callers should skip emitting Mcp-Name
// in that case (server-side fail-closed will reject mismatches).
//
// params is the Go value the caller passed to Call / rawCallWithContext —
// typically map[string]any or a concrete struct. Supports both shapes so
// no caller has to flatten ahead of time.
func deriveMcpName(method string, params any) string {
	if params == nil {
		return ""
	}
	switch method {
	case "tools/call", "prompts/get":
		return stringField(params, "name")
	case "resources/read":
		return stringField(params, "uri")
	}
	return ""
}

// stringField reads a string-typed field from a params value by name. Handles
// the common map[string]any and map[string]string shapes; struct-typed params
// are out of scope here (the routing-header helpers only fire for the three
// SEP-2243 methods, and mcpkit's call sites for those use map shapes).
func stringField(params any, field string) string {
	switch p := params.(type) {
	case map[string]any:
		if v, ok := p[field].(string); ok {
			return v
		}
	case map[string]string:
		return p[field]
	}
	return ""
}


// SEP-2243 custom-header mirroring for tools/call.
//
// When a tool's inputSchema marks a primitive-typed property with the
// `x-mcp-header` keyword, the client MUST send the corresponding argument
// value as an `Mcp-Param-{HeaderName}` HTTP header on the tools/call
// request — in addition to including it in the JSON body. The header name
// is the keyword's value verbatim; the wire header is HTTP-case-insensitive.
//
// Values are encoded per SEP-2243 §value-encoding:
//   - Plain ASCII (0x20-0x7E, no leading/trailing whitespace, no tab/CR/LF):
//     sent verbatim.
//   - Anything else (non-ASCII, control chars, tab, leading/trailing
//     whitespace): wrapped as `=?base64?{base64-utf8}?=`.
//   - Numbers: string representation of the value.
//   - Booleans: "true" or "false".
//   - Null/undefined: header omitted entirely.
//
// Spec: https://modelcontextprotocol.io/specification/draft/basic/transports#custom-headers-from-tool-parameters

// extractMcpParamHeaders walks a tool's inputSchema and returns a map from
// property name to header-name fragment. The HTTP header on the wire is
// `Mcp-Param-{name fragment}`.
//
// Only primitive-typed properties (string/number/integer/boolean) participate
// per the SEP-2243 spec; properties with `x-mcp-header` on object/array/null
// types are ignored here (they're tracked as tool-validation failures, see
// SEP-2243 invalid-tool-headers scenario — out of scope for this helper).
//
// Returns an empty map for nil schemas or schemas without any `x-mcp-header`
// annotations. Never returns an error — malformed schemas just yield empty
// maps so callers can proceed without headers (safe default).
func extractMcpParamHeaders(inputSchema any) map[string]string {
	out := map[string]string{}
	schema, ok := inputSchema.(map[string]any)
	if !ok {
		return out
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return out
	}
	for propName, raw := range props {
		propMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		headerName, ok := propMap["x-mcp-header"].(string)
		if !ok || headerName == "" {
			continue
		}
		// SEP-2243: only primitive types may carry x-mcp-header. The invalid-
		// tool-headers scenario covers rejecting non-primitive cases at
		// tools/list time; here we just skip them so a misannotated tool
		// doesn't generate bogus headers.
		switch propMap["type"] {
		case "string", "number", "integer", "boolean":
			out[propName] = headerName
		}
	}
	return out
}

// encodeMcpParamHeaderValue produces the wire form of an x-mcp-header value
// per SEP-2243 §value-encoding. Returns the encoded string and ok=true if a
// header should be sent for this value, or ok=false if the header should be
// omitted entirely (nil arguments).
//
// String values are emitted verbatim when they're plain ASCII without
// edge-whitespace or control chars; otherwise wrapped as
// `=?base64?{base64-utf8}?=`. Numbers use the shortest float-round-trip
// representation. Booleans use "true"/"false". Nil omits the header.
func encodeMcpParamHeaderValue(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch val := v.(type) {
	case string:
		return encodeMcpParamString(val), true
	case bool:
		if val {
			return "true", true
		}
		return "false", true
	case int:
		return strconv.Itoa(val), true
	case int32:
		return strconv.FormatInt(int64(val), 10), true
	case int64:
		return strconv.FormatInt(val, 10), true
	case float32:
		return encodeMcpParamNumber(float64(val)), true
	case float64:
		return encodeMcpParamNumber(val), true
	default:
		// Last-resort: stringify via %v. Won't be base64-wrapped because we
		// can't tell what kind of bytes it produces; if it contains unsafe
		// chars the conformance check will catch it.
		return fmt.Sprintf("%v", val), true
	}
}

// encodeMcpParamString applies SEP-2243 encoding to a string value: plain
// ASCII passes through; anything that needs encoding gets wrapped as
// `=?base64?{...}?=`.
func encodeMcpParamString(s string) string {
	if needsBase64Encoding(s) {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
	}
	return s
}

// encodeMcpParamNumber converts a float64 to the shortest accurate string
// representation. Integers come out without a decimal point ("42"); non-
// integer floats keep enough precision to round-trip ("3.14159").
func encodeMcpParamNumber(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// needsBase64Encoding reports whether a string value must be base64-wrapped
// for SEP-2243 header transport. Triggers:
//   - any byte outside printable ASCII range (0x20-0x7E)
//   - any tab character (0x09)
//   - leading or trailing whitespace (including spaces)
//
// Internal whitespace within the visible-ASCII range is fine (plain ASCII).
func needsBase64Encoding(s string) bool {
	if s == "" {
		return false
	}
	if s != strings.TrimSpace(s) {
		return true
	}
	for _, r := range s {
		if r == '\t' {
			return true
		}
		if r < 0x20 || r > 0x7e {
			return true
		}
	}
	return false
}

// mcpParamHeaderName returns the wire HTTP header name for a given x-mcp-header
// fragment. The HTTP header is case-insensitive but spec-canonical form is
// `Mcp-Param-{fragment}`.
func mcpParamHeaderName(fragment string) string {
	return "Mcp-Param-" + fragment
}

// validateMcpParamHeaders verifies a tool's inputSchema against SEP-2243's
// rules for `x-mcp-header` annotations. Returns nil if every annotation is
// spec-compliant (or the schema has no annotations at all), or an error
// describing the first violation if any property breaks the rules. Used by
// Client.ListTools to filter out tools whose schemas cannot be safely called.
//
// Rules per SEP-2243 §custom-headers-from-tool-parameters:
//   - The keyword value MUST be a non-empty string.
//   - The keyword MUST only appear on primitive-typed properties
//     (string / number / integer / boolean).
//   - The keyword value MUST contain only printable-ASCII chars excluding
//     space, colon, tab, control chars, and non-ASCII.
//   - The keyword values within a single tool MUST be unique, case-insensitive
//     (e.g. "MyField" and "myfield" collide and the tool is invalid).
//
// Schemas not shaped as `{properties: {name: {...}}}` (or `inputSchema` nil)
// return nil — there's nothing to validate.
func validateMcpParamHeaders(inputSchema any) error {
	schema, ok := inputSchema.(map[string]any)
	if !ok {
		return nil
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	seen := map[string]string{} // lowercased value → first property name that used it
	for propName, raw := range props {
		propMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawHeader, present := propMap["x-mcp-header"]
		if !present {
			continue
		}
		headerName, ok := rawHeader.(string)
		if !ok {
			return fmt.Errorf("property %q: x-mcp-header value must be a string", propName)
		}
		if headerName == "" {
			return fmt.Errorf("property %q: x-mcp-header value must not be empty", propName)
		}
		propType, _ := propMap["type"].(string)
		switch propType {
		case "string", "number", "integer", "boolean":
			// ok
		default:
			return fmt.Errorf("property %q: x-mcp-header may only appear on primitive types (got %q)", propName, propType)
		}
		if err := validateMcpHeaderName(headerName); err != nil {
			return fmt.Errorf("property %q: %w", propName, err)
		}
		key := strings.ToLower(headerName)
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("property %q: x-mcp-header %q collides with property %q (case-insensitive)", propName, headerName, prev)
		}
		seen[key] = propName
	}
	return nil
}

// validateMcpHeaderName enforces the SEP-2243 charset rule for an x-mcp-header
// value: ASCII-only, no space, colon, tab, or control characters.
func validateMcpHeaderName(s string) error {
	for _, r := range s {
		switch {
		case r > 0x7e:
			return fmt.Errorf("x-mcp-header %q contains non-ASCII char %q", s, r)
		case r < 0x20:
			return fmt.Errorf("x-mcp-header %q contains control char (0x%02x)", s, r)
		case r == ' ':
			return fmt.Errorf("x-mcp-header %q contains a space", s)
		case r == ':':
			return fmt.Errorf("x-mcp-header %q contains a colon", s)
		case r == '\t':
			return fmt.Errorf("x-mcp-header %q contains a tab", s)
		}
	}
	return nil
}
