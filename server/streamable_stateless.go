package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

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
//     _meta[protocolVersion]. Disagreement → -32020 + HTTP 400.
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
	// when both are present → -32020 HeaderMismatch.
	if hdrVer := r.Header.Get(mcpProtocolVersionHeader); hdrVer != "" {
		metaVer := peekMetaProtocolVersion(req.Params)
		if mismatchResp := statelessVersionMismatch(id, hdrVer, metaVer); mismatchResp != nil {
			writeStatelessResponse(w, mismatchResp)
			return
		}
	}

	// (1b) SEP-2243 routing-header validation. The stateless wire only
	// speaks 2026-07-28 (the version that adopted SEP-2243), so any
	// Mcp-Method / Mcp-Name a client does send MUST agree with the body.
	// Lenient on absent headers — keeps clients that haven't adopted
	// SEP-2243 yet working — strict on mismatched values, which is what
	// the conformance suite locks. Header *presence* without a match
	// surfaces -32020 via headerMismatchResponse with the diagnostic
	// `data` payload (header, expected, received).
	if r.Header.Get(core.McpMethodHeader) != "" {
		if errResp := validateRoutingHeaders(req, r.Header); errResp != nil {
			writeHeaderMismatch(w, errResp)
			return
		}
	}

	// (2) Dispatch. ResponseHeaderCollector lets handlers stage
	// transport-level headers (SEP-2243 Mcp-Name etc.) the same way
	// the legacy path does. WithStatelessClaims threads the
	// CheckAuth-produced principal onto ctx so handlers reach it via
	// ctx.AuthClaims() — legacy parity, where claims live on the
	// sessionCtx that the stateless wire deliberately does not create.
	ctx := core.WithResponseHeaderCollector(r.Context())
	ctx = core.WithStatelessClaims(ctx, claims)
	resp, dErr := t.statelessDispatcher.Dispatch(ctx, req)

	// A non-nil dispatch error is a middleware short-circuit (typically
	// *core.AuthError). Surface it through the shared writeAuthError so the
	// stateless wire emits the same HTTP 403 + WWW-Authenticate as the legacy
	// wire instead of a generic -32603/HTTP 200 body (issue 815).
	if dErr != nil {
		writeAuthError(w, dErr)
		return
	}

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

// handleStatelessPostSSE is the SEP-2575 dispatch path for a single
// JSON-RPC request whose Accept header asked for text/event-stream
// (issue #753). The POST response itself becomes the SSE channel:
// handler-emitted notifications via ctx.Notify(...) are framed as
// SSE events on the open response stream; the handler's terminal
// *core.Response is the final SSE event before the server closes
// the connection.
//
// Mirrors the legacy handlePostSSE shape — same lazy-headers + flusher
// + serialized writes + closed-after-handler-return safety — but
// simpler because the stateless wire has no event store to replay,
// no session ID to namespace event IDs under, no server-to-client
// request channel (sampling/elicitation are not available on the
// stateless wire by SEP-2575 spec), and no retry hint to forward to a
// long-lived GET stream.
//
// Notifications get framed via WithStatelessNotifyFunc on ctx so
// BaseContext.Notify resolves them through the stateless fallback
// branch. The events/stream handler — and any other streaming-shaped
// custom method registered via Server.HandleMethod — works
// identically on either wire.
func (t *streamableTransport) handleStatelessPostSSE(w http.ResponseWriter, r *http.Request, claims *core.Claims, req *core.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Fallback to synchronous JSON for the rare ResponseWriter
		// that doesn't support flushing — same defensive shape as
		// the legacy path.
		t.handleStatelessPost(w, r, claims, req)
		return
	}

	dispatchCtx := core.WithResponseHeaderCollector(r.Context())
	dispatchCtx = core.WithStatelessClaims(dispatchCtx, claims)

	// SSE headers are set lazily on first write so a dispatch-time
	// error response (-32020 header mismatch, -32022 unsupported
	// protocol version, -32601 method not found from the bare default
	// branch) can still be surfaced as a normal JSON-RPC response over
	// HTTP 200 (the legacy path stages an HTTP-level auth error
	// instead; stateless has no such transport-error path).
	var mu sync.Mutex
	var closed bool
	var sseStarted bool
	writeSSE := func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		if !sseStarted {
			applyStagedResponseHeaders(w, dispatchCtx)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			sseStarted = true
		}
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
	}

	requestNotify := core.NotifyFunc(func(method string, params any) {
		raw, err := core.MarshalNotification(method, params)
		if err != nil {
			return
		}
		writeSSE(raw)
	})
	dispatchCtx = core.WithStatelessNotifyFunc(dispatchCtx, requestNotify)

	// Header / _meta version cross-check (mirrored from handleStatelessPost).
	if hdrVer := r.Header.Get(mcpProtocolVersionHeader); hdrVer != "" {
		metaVer := peekMetaProtocolVersion(req.Params)
		if mismatchResp := statelessVersionMismatch(req.ID, hdrVer, metaVer); mismatchResp != nil {
			writeHeaderMismatch(w, mismatchResp)
			return
		}
	}

	// SEP-2243 routing-header validation (mirrored from handleStatelessPost).
	if r.Header.Get(core.McpMethodHeader) != "" {
		if errResp := validateRoutingHeaders(req, r.Header); errResp != nil {
			writeHeaderMismatch(w, errResp)
			return
		}
	}

	resp, dErr := t.statelessDispatcher.Dispatch(dispatchCtx, req)

	// A middleware short-circuit (typically *core.AuthError) fires before the
	// handler runs, so no SSE frame has been written yet (sseStarted is false).
	// Surface it as an HTTP-level auth error via the shared writeAuthError —
	// same 403 + WWW-Authenticate the legacy and non-SSE stateless paths emit
	// (issue 815). The mu/closed guard keeps writeSSE a no-op afterward.
	if dErr != nil {
		mu.Lock()
		started := sseStarted
		mu.Unlock()
		if !started {
			writeAuthError(w, dErr)
		}
		mu.Lock()
		closed = true
		mu.Unlock()
		return
	}

	// Terminal frame. resp is normally non-nil for SSE-eligible requests
	// (notifications return 202 from handleStatelessPost and never reach
	// here — shouldStreamSSE short-circuits on IsNotification first).
	// Belt-and-suspenders nil-check matches handlePostSSE.
	if resp != nil {
		if sseStarted {
			raw, _ := marshalJSON(resp)
			writeSSE(raw)
		} else {
			// Dispatcher returned an error before the handler emitted
			// any notification — surface as a normal JSON response.
			// Stateless-wire HTTP status mapping (HTTPStatusForCode)
			// still applies.
			applyStagedResponseHeaders(w, dispatchCtx)
			writeStatelessResponse(w, resp)
		}
	}

	mu.Lock()
	closed = true
	mu.Unlock()
}

// writeStatelessResponse marshals a JSON-RPC response and writes it
// with the SEP-2575-mandated HTTP status. Success responses go out
// with 200; error responses use stateless.HTTPStatusForCode so
// -32601→404, -32020/-32021/-32022/-32602/-32700/-32600→400, etc.
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
