package client

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

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
