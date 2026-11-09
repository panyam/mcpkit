package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// Whether a negotiated protocol version mandates SEP-2243 routing-header
// validation lives in the version feature-set resolver
// (ProtocolFeatures.RoutingHeaderValidation in protocol_features.go), so all
// version gating stays in one table.

// validateRoutingHeaders enforces SEP-2243 §Server Validation: the
// `Mcp-Method` header MUST be present and exactly match the JSON-RPC
// body method, and for methods that carry a body-side identifier
// (see extractRoutingName) the `Mcp-Name` header MUST be present and match.
// Header values are trimmed for RFC 9110 OWS, and a Base64-sentinel
// `Mcp-Name` is decoded, before comparison; one that will not decode fails.
//
// Returns a JSON-RPC error frame on mismatch (caller writes HTTP 400 +
// the frame as the body). Returns nil when the request passes.
func validateRoutingHeaders(req *core.Request, headers http.Header) *core.Response {
	hdrMethod := strings.TrimSpace(headers.Get(core.McpMethodHeader))
	if hdrMethod != req.Method {
		return headerMismatchResponse(req.ID, core.McpMethodHeader, hdrMethod, req.Method)
	}
	field, bodyValue, requiresName := extractRoutingName(req)
	if !requiresName {
		return nil
	}
	hdrName := strings.TrimSpace(headers.Get(core.McpNameHeader))
	decoded, err := core.DecodeMcpHeaderValue(hdrName)
	if err != nil {
		return headerMismatchResponse(req.ID, core.McpNameHeader, hdrName, bodyValue,
			"bodyField", field, "decodeError", err.Error())
	}
	if decoded != bodyValue {
		return headerMismatchResponse(req.ID, core.McpNameHeader, hdrName, bodyValue,
			"bodyField", field)
	}
	return nil
}

// extractRoutingName reports whether the request's method carries a
// body-side identifier that `Mcp-Name` must mirror, and returns the
// expected value. Methods without a name-shaped param leave Mcp-Name
// validation off — the spec only mandates Mcp-Name for calls with a
// resource-routable name in the body.
func extractRoutingName(req *core.Request) (field, value string, ok bool) {
	switch req.Method {
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return "name", "", true
		}
		return "name", p.Name, true
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return "uri", "", true
		}
		return "uri", p.URI, true
	case "prompts/get":
		// SEP-2243 lists prompts/get among the name-carrying methods
		// (params.name); mcpkit's own client stamps Mcp-Name for it via
		// core.DeriveMcpName, so the server must validate it symmetrically.
		var p struct {
			Name string `json:"name"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return "name", "", true
		}
		return "name", p.Name, true
	case "events/poll", "events/stream", "events/subscribe", "events/unsubscribe":
		// SEP-3415 § Streamable HTTP: Routing Headers. events/list has no
		// params.name and carries no Mcp-Name, so it falls to default.
		var p struct {
			Name string `json:"name"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return "name", "", true
		}
		return "name", p.Name, true
	case "tasks/get", "tasks/update", "tasks/cancel":
		// SEP-2663 elevates Mcp-Name: <taskId> to a required client
		// header on these methods, so SEP-2243's universal MUST applies.
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return "taskId", "", true
		}
		return "taskId", p.TaskID, true
	default:
		return "", "", false
	}
}

// writeHeaderMismatch writes an HTTP 400 response carrying the
// JSON-RPC error frame from validateRoutingHeaders. Per SEP-2243 the
// status code is MUST (400) and the JSON-RPC error code is SHOULD
// (ErrCodeHeaderMismatch); we emit both so the conformance warning
// checks also flip to SUCCESS.
func writeHeaderMismatch(w http.ResponseWriter, errResp *core.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	raw, err := json.Marshal(errResp)
	if err != nil {
		// Fall back to a plain JSON shape; should never happen for our
		// own well-formed Response value, but don't drop the status code.
		// Build the code from the constant so this path can never drift
		// from ErrCodeHeaderMismatch.
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":null,"error":{"code":%d,"message":"header mismatch"}}`,
			core.ErrCodeHeaderMismatch)))
		return
	}
	_, _ = w.Write(raw)
}

// headerMismatchResponse builds the JSON-RPC error frame for a SEP-2243
// routing-header validation failure. The `data` object carries
// `reason` / `header` / `expected` / `received` so client transports
// (and humans) can see exactly which header disagreed with what value.
func headerMismatchResponse(id json.RawMessage, header, received, expected string, extra ...string) *core.Response {
	reason := fmt.Sprintf("%s header value does not match request body", header)
	if received == "" {
		reason = fmt.Sprintf("%s header is required but missing", header)
	}
	data := map[string]any{
		"reason":   reason,
		"header":   header,
		"expected": expected,
		"received": received,
	}
	for i := 0; i+1 < len(extra); i += 2 {
		data[extra[i]] = extra[i+1]
	}
	return core.NewErrorResponseWithData(id, core.ErrCodeHeaderMismatch, reason, data)
}

// validateParamHeaders enforces SEP-2243 §Server Behavior for Custom Headers
// on tools/call. For each argument the tool's inputSchema marks with
// `x-mcp-header`, the `Mcp-Param-{Name}` header must mirror the body:
//
//   - a value in the body needs the header, and a header needs a value in the
//     body (an omitted or null optional argument has neither);
//   - a required annotated argument missing from the body fails here, as the
//     SEP's "MUST reject with JSON-RPC error" asks, rather than becoming the
//     isError tool result other schema failures get (#1111);
//   - the header is decoded with core.DecodeMcpHeaderValue, and a plain value
//     a client should have Base64-wrapped fails as invalid characters;
//   - numbers compare numerically, booleans as "true"/"false".
//
// Mcp-Param headers no annotation names are ignored, as are unknown tools,
// which the dispatcher answers. Returns nil when the request passes, or the
// -32020 frame for writeHeaderMismatch.
func validateParamHeaders(req *core.Request, headers http.Header, reg *Registry) *core.Response {
	if req.Method != "tools/call" {
		return nil
	}
	var p struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if err := req.Params.Bind(&p); err != nil {
		return nil
	}
	def, ok := reg.ToolDef(p.Name)
	if !ok {
		return nil
	}
	annotated := core.ExtractMcpHeaderParams(def.InputSchema)
	if len(annotated) == 0 {
		return nil
	}
	required := map[string]bool{}
	if schema, ok := def.InputSchema.(map[string]any); ok {
		switch r := schema["required"].(type) {
		case []any:
			for _, n := range r {
				if s, ok := n.(string); ok {
					required[s] = true
				}
			}
		case []string:
			for _, s := range r {
				required[s] = true
			}
		}
	}

	props := make([]string, 0, len(annotated))
	for prop := range annotated {
		props = append(props, prop)
	}
	sort.Strings(props)
	for _, prop := range props {
		header := core.McpParamHeaderName(annotated[prop])
		values := headers.Values(header)
		body, inBody := p.Arguments[prop]
		inBody = inBody && string(body) != "null"
		bodyField := "arguments." + prop

		switch {
		case !inBody && required[prop]:
			reason := fmt.Sprintf("required parameter %q is missing", prop)
			return core.NewErrorResponseWithData(req.ID, core.ErrCodeHeaderMismatch, reason, map[string]any{
				"reason": reason, "header": header, "bodyField": bodyField,
			})
		case !inBody && len(values) == 0:
			continue
		case !inBody:
			return headerMismatchResponse(req.ID, header, values[0], "", "bodyField", bodyField)
		case len(values) == 0:
			return headerMismatchResponse(req.ID, header, "", paramBodyText(body), "bodyField", bodyField)
		}

		received := strings.TrimSpace(values[0])
		decoded, err := core.DecodeMcpHeaderValue(received)
		if err == nil && decoded == received && !isVisibleASCII(received) {
			err = fmt.Errorf("header value %q has characters that need Base64 encoding", received)
		}
		if err != nil {
			return headerMismatchResponse(req.ID, header, received, paramBodyText(body),
				"bodyField", bodyField, "decodeError", err.Error())
		}
		if !paramHeaderMatches(decoded, body) {
			return headerMismatchResponse(req.ID, header, received, paramBodyText(body), "bodyField", bodyField)
		}
	}
	return nil
}

// paramHeaderMatches compares a decoded Mcp-Param value to the JSON body value
// it mirrors: strings exactly, numbers numerically (so "5.0" matches 5), and
// booleans as "true" or "false".
func paramHeaderMatches(decoded string, body json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	switch b := v.(type) {
	case string:
		return decoded == b
	case float64:
		f, err := strconv.ParseFloat(decoded, 64)
		return err == nil && f == b
	case bool:
		return decoded == strconv.FormatBool(b)
	default:
		return false
	}
}

// isVisibleASCII reports whether s is printable ASCII (0x20-0x7E), the only
// characters a plain, unencoded Mcp-Param value may carry.
func isVisibleASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// paramBodyText renders a body value for a -32020 error's "expected" field:
// a JSON string without its quotes, anything else as raw JSON.
func paramBodyText(body json.RawMessage) string {
	var str string
	if json.Unmarshal(body, &str) == nil {
		return str
	}
	return string(body)
}
