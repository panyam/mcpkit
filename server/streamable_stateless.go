package server

import (
	"encoding/json"
	"net/http"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// handleStatelessPost is the SEP-2575 dispatch path for a single
// (non-batch, non-response) JSON-RPC request that detectWireKind
// has classified as wireStateless. Separate from handlePost so the
// legacy path stays uncluttered; deletion of the legacy wire later
// removes the routing branch + the legacy method on handlePost,
// leaving this one untouched.
//
// Flow:
//
//  1. Cross-check MCP-Protocol-Version HTTP header against
//     _meta[protocolVersion]. Disagreement → -32001 + HTTP 400.
//  2. Dispatch via t.statelessDispatcher.Dispatch (which itself
//     validates _meta and protocol-version against the supported
//     list, threads RequestMeta through ctx, and routes by method).
//  3. Map JSON-RPC code → HTTP status via stateless.HTTPStatusForCode
//     and write the body. Notifications (resp == nil) → 202.
//
// Claims are validated upstream in handlePost via t.server.CheckAuth;
// this handler trusts them as a precondition.
func (t *streamableTransport) handleStatelessPost(w http.ResponseWriter, r *http.Request, claims *core.Claims, req *core.Request) {
	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}

	// (1) Header / _meta version cross-check. Header may be absent
	// (some clients only stamp _meta); _meta-only is fine. Mismatch
	// when both are present → -32001 HeaderMismatch.
	if hdrVer := r.Header.Get(mcpProtocolVersionHeader); hdrVer != "" {
		metaVer := peekMetaProtocolVersion(req.Params)
		if mismatchResp := statelessVersionMismatch(id, hdrVer, metaVer); mismatchResp != nil {
			writeStatelessResponse(w, mismatchResp)
			return
		}
	}

	// (2) Dispatch. ResponseHeaderCollector lets handlers stage
	// transport-level headers (SEP-2243 Mcp-Name etc.) the same way
	// the legacy path does.
	ctx := core.WithResponseHeaderCollector(r.Context())
	_ = claims // claims are used by extension middleware downstream; placeholder for now
	resp := t.statelessDispatcher.Dispatch(ctx, req)

	if resp == nil {
		// Notification (no id) — return 202 Accepted with no body.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	applyStagedResponseHeaders(w, ctx)
	writeStatelessResponse(w, resp)
}

// peekMetaProtocolVersion returns the protocolVersion advertised in
// the _meta envelope, or "" if absent. Used by the header cross-check
// without re-running the full DecodeRequestMeta path (the dispatcher
// will run that and return -32602 if structurally missing).
func peekMetaProtocolVersion(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var probe struct {
		Meta struct {
			ProtocolVersion string `json:"io.modelcontextprotocol/protocolVersion"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &probe); err != nil {
		return ""
	}
	return probe.Meta.ProtocolVersion
}

// writeStatelessResponse marshals a JSON-RPC response and writes it
// with the SEP-2575-mandated HTTP status. Success responses go out
// with 200; error responses use stateless.HTTPStatusForCode so
// -32601→404, -32001/-32003/-32004/-32602/-32700/-32600→400, etc.
//
// Headers (Content-Type) MUST be set before WriteHeader; the
// applyStagedResponseHeaders call upstream of us already stamped
// any transport headers the handler asked for.
func writeStatelessResponse(w http.ResponseWriter, resp *core.Response) {
	w.Header().Set("Content-Type", "application/json")
	status := 200
	if resp.Error != nil {
		status = stateless.HTTPStatusForCode(resp.Error.Code)
	}
	w.WriteHeader(status)
	raw, _ := marshalJSON(resp)
	w.Write(raw)
}
