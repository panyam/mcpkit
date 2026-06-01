package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// sep2243EnforcedVersions enumerates the negotiated MCP protocol
// versions that mandate SEP-2243 routing-header validation server-side.
// SEP-2243 is currently draft-only; widen this list when a dated
// release picks up the requirement (and the official SDKs ship clients
// that emit Mcp-Method / Mcp-Name).
var sep2243EnforcedVersions = map[string]bool{
	"DRAFT-2026-v1": true,
}

// isSEP2243EnforcedVersion reports whether the session's negotiated
// protocol version requires Mcp-Method / Mcp-Name validation.
func isSEP2243EnforcedVersion(negotiated string) bool {
	return sep2243EnforcedVersions[negotiated]
}

// validateRoutingHeaders enforces SEP-2243 §Server Validation: the
// `Mcp-Method` header MUST be present and exactly match the JSON-RPC
// body method, and for methods that carry a body-side identifier
// (currently `tools/call` → `params.name`, `resources/read` →
// `params.uri`) the `Mcp-Name` header MUST be present and match.
// Header values are trimmed for RFC 9110 OWS before comparison.
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
	if hdrName != bodyValue {
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
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return "name", "", true
		}
		return "name", p.Name, true
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return "uri", "", true
		}
		return "uri", p.URI, true
	case "tasks/get", "tasks/update", "tasks/cancel":
		// SEP-2663 elevates Mcp-Name: <taskId> to a required client
		// header on these methods, so SEP-2243's universal MUST applies.
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
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
// (-32001); we emit both so the conformance warning checks also flip
// to SUCCESS.
func writeHeaderMismatch(w http.ResponseWriter, errResp *core.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	raw, err := json.Marshal(errResp)
	if err != nil {
		// Fall back to a plain JSON shape; should never happen for our
		// own well-formed Response value, but don't drop the status code.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"header mismatch"}}`))
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
