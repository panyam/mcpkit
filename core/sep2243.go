package core

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// SEP-2243 HTTP-header wire format.
//
// SEP-2243 ("Standard MCP Request Headers") layers two HTTP-header concerns
// on top of the JSON-RPC envelope:
//
//   - Standard routing headers — `Mcp-Method` (always present) and `Mcp-Name`
//     (for methods whose params carry a routable name/URI: tools/call,
//     prompts/get, resources/read). Lets proxies and middleware route without
//     parsing the JSON body. See [McpMethodHeader], [McpNameHeader],
//     [DeriveMcpName].
//
//   - Custom parameter mirroring — `Mcp-Param-{Name}` headers that mirror
//     tool arguments annotated with `x-mcp-header` in the tool's inputSchema.
//     The wire form is HTTP-case-insensitive; the canonical spelling is
//     `Mcp-Param-{Name}`. See [McpParamHeaderName], [ExtractMcpHeaderParams],
//     [ValidateMcpHeaderSchema], [EncodeMcpHeaderValue].
//
// Both surfaces share one value-encoding rule (SEP-2243 §value-encoding):
// plain printable ASCII goes verbatim; anything else is wrapped as
// `=?base64?{base64-utf8}?=`. See [EncodeMcpHeaderValue] +
// [NeedsBase64Encoding].
//
// Helpers in this file are pure spec rules — no I/O, no http.Request
// mutation. Callers that need outbound-request mutation live one layer up
// (e.g. client/headers.go::setSEP2243RoutingHeaders). Future server-side
// validation/decoding will consume the same rules from this file.
//
// Spec: https://modelcontextprotocol.io/specification/draft/basic/transports#custom-headers-from-tool-parameters

// McpMethodHeader is the SEP-2243 standard routing header name carrying the
// JSON-RPC method. Streamable HTTP transports set it on every outbound
// request; servers MAY use it to route without parsing the body.
const McpMethodHeader = "Mcp-Method"

// McpNameHeader is the SEP-2243 standard routing header name carrying the
// per-method routable identifier — `params.name` for tools/call and
// prompts/get, `params.uri` for resources/read. Absent for methods whose
// params have no such field.
const McpNameHeader = "Mcp-Name"

// DeriveMcpName extracts the [McpNameHeader] value from a JSON-RPC params
// payload for the three methods that carry a routable name/URI: tools/call
// (params.name), prompts/get (params.name), and resources/read (params.uri).
// Returns "" for any other method or when the field is missing / non-string
// — callers should skip emitting Mcp-Name in that case (server-side
// fail-closed will reject mismatches).
//
// params is the Go value the producer holds — typically `map[string]any`
// or `map[string]string`. Other shapes (structs, json.RawMessage) return
// "" — callers that need struct-typed support should marshal/unmarshal to
// a map first.
func DeriveMcpName(method string, params any) string {
	if params == nil {
		return ""
	}
	switch method {
	case "tools/call", "prompts/get":
		return mcpParamsStringField(params, "name")
	case "resources/read":
		return mcpParamsStringField(params, "uri")
	}
	return ""
}

func mcpParamsStringField(params any, field string) string {
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

// McpParamHeaderName returns the wire HTTP header name for an `x-mcp-header`
// fragment. The HTTP header is case-insensitive but the spec-canonical
// spelling is `Mcp-Param-{fragment}`.
func McpParamHeaderName(fragment string) string {
	return "Mcp-Param-" + fragment
}

// ExtractMcpHeaderParams walks a tool's inputSchema and returns a map from
// property name to header-name fragment. The HTTP header on the wire is
// `Mcp-Param-{fragment}` (see [McpParamHeaderName]).
//
// Only primitive-typed properties (string/number/integer/boolean)
// participate per the SEP-2243 spec; properties with `x-mcp-header` on
// object/array/null types are ignored here. Tool-registration validation
// (see [ValidateMcpHeaderSchema]) catches misannotations at registration
// time; this extractor is producer-side and stays tolerant so a
// misannotated tool yields no headers rather than panicking.
//
// Returns an empty (non-nil) map for nil schemas or schemas without any
// `x-mcp-header` annotations. Never returns an error.
func ExtractMcpHeaderParams(inputSchema any) map[string]string {
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
		switch propMap["type"] {
		case "string", "number", "integer", "boolean":
			out[propName] = headerName
		}
	}
	return out
}

// ValidateMcpHeaderSchema verifies a tool's inputSchema against SEP-2243's
// rules for `x-mcp-header` annotations. Returns nil if every annotation is
// spec-compliant (or the schema has no annotations at all), or an error
// describing the first violation if any property breaks the rules.
//
// Rules per SEP-2243 §custom-headers-from-tool-parameters:
//
//   - The keyword value MUST be a non-empty string.
//   - The keyword MUST only appear on primitive-typed properties
//     (string / number / integer / boolean).
//   - The keyword value MUST contain only printable-ASCII chars excluding
//     space, colon, tab, control chars, and non-ASCII.
//   - The keyword values within a single tool MUST be unique
//     case-insensitively (e.g. "MyField" and "myfield" collide and the
//     tool is invalid).
//
// Schemas not shaped as `{properties: {name: {...}}}` (or nil inputSchema)
// return nil — there's nothing to validate.
func ValidateMcpHeaderSchema(inputSchema any) error {
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

// EncodeMcpHeaderValue produces the wire form of an SEP-2243 header value
// per §value-encoding. Returns the encoded string and ok=true if a header
// should be sent for this value, or ok=false if the header should be
// omitted entirely (nil arguments).
//
// String values are emitted verbatim when they're plain ASCII without
// edge-whitespace or control chars; otherwise wrapped as
// `=?base64?{base64-utf8}?=`. Numbers use the shortest round-trip
// representation. Booleans use "true"/"false". Nil omits the header.
// Other types fall through to `fmt.Sprintf("%v", v)`; the conformance
// suite catches unsafe results.
func EncodeMcpHeaderValue(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch val := v.(type) {
	case string:
		return encodeMcpHeaderString(val), true
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
		return encodeMcpHeaderNumber(float64(val)), true
	case float64:
		return encodeMcpHeaderNumber(val), true
	default:
		return fmt.Sprintf("%v", val), true
	}
}

func encodeMcpHeaderString(s string) string {
	if NeedsBase64Encoding(s) {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
	}
	return s
}

func encodeMcpHeaderNumber(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// NeedsBase64Encoding reports whether a string value must be base64-wrapped
// for SEP-2243 header transport. Triggers:
//
//   - any byte outside printable ASCII range (0x20-0x7E)
//   - any tab character (0x09)
//   - leading or trailing whitespace (including spaces)
//
// Internal whitespace within the visible-ASCII range is fine (plain ASCII).
// Exported so server-side decoders and external producers can apply the
// same predicate without duplicating it.
func NeedsBase64Encoding(s string) bool {
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
