package server

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	case "prompts/get":
		// SEP-2243 lists prompts/get among the name-carrying methods
		// (params.name); mcpkit's own client stamps Mcp-Name for it via
		// core.DeriveMcpName, so the server must validate it symmetrically.
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return "name", "", true
		}
		return "name", p.Name, true
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
