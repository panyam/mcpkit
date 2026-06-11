package core

import "net/http"

// HTTP context forwarding — the outbound RoundTripper helper from
// the SEP-2028 scope. Wraps an http.RoundTripper so a tool handler's
// downstream HTTP calls automatically carry the W3C Trace Context
// and W3C Baggage values that arrived on the inbound MCP request,
// closing the end-to-end observability loop for handlers that call
// third-party HTTP APIs (the AccuWeather case in the SEP motivation).
//
// What this helper does NOT do:
//   - Configure header groups, policies, or per-tool opt-outs. The
//     SEP-2028 `headerGroups` API is deferred until the upstream
//     spec settles (mcpkit issue 739).
//   - Forward arbitrary `_meta` fields. Only the W3C-defined fields
//     (`traceparent` / `tracestate` / `baggage`) ride out by default;
//     custom MCP-side fields would risk leaking business context to
//     third-party APIs without explicit opt-in (the SEP's same
//     reasoning).
//   - Inject anything when ctx carries no context. Zero overhead on
//     the unconfigured path — if a handler's ctx has no
//     TraceContext and no Baggage, RoundTrip is a straight pass-through.
//
// Spec references:
//   - W3C Trace Context: https://www.w3.org/TR/trace-context/
//   - W3C Baggage:       https://www.w3.org/TR/baggage/
//   - SEP-2028 (upstream tracking — mcpkit issue 739).

// HTTP header names per W3C — Go's net/http canonicalizes header
// keys to title-case on Set; both the canonical and the W3C-spec
// lowercase forms read identically via http.Header.Get
// (case-insensitive). These constants document the spec name and
// give a single edit point if a future version renames them.
const (
	HTTPHeaderTraceparent = "Traceparent"
	HTTPHeaderTracestate  = "Tracestate"
	HTTPHeaderBaggage     = "Baggage"
)

// HTTPForwardTransport wraps base so every outgoing request
// automatically carries the W3C Trace Context (`Traceparent` /
// `Tracestate`) and W3C Baggage (`Baggage`) headers derived from
// the request's Context. Tool handlers compose this into an
// http.Client they use for downstream API calls:
//
//	client := &http.Client{
//	    Transport: core.HTTPForwardTransport(http.DefaultTransport),
//	}
//	// inside a tool handler:
//	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.example.com", nil)
//	resp, err := client.Do(req)
//
// Behavior:
//
//   - Reads core.TraceContextFromContext(req.Context()) and
//     core.BaggageFromContext(req.Context()); when both are zero,
//     the request is forwarded to base untouched (zero allocation,
//     no header mutation).
//   - When at least one is non-zero, the request is cloned (per the
//     http.RoundTripper contract: implementations MUST NOT modify
//     the request) and the corresponding headers are stamped on the
//     clone.
//   - Caller-set headers WIN. If the request already carries a
//     `Traceparent` / `Tracestate` / `Baggage` header set by user
//     code, the existing value is preserved — the wrap never
//     clobbers explicit intent.
//   - A nil base is replaced with http.DefaultTransport so callers
//     can construct ad-hoc transports without checking. Matches the
//     net/http stdlib defaulting convention.
//
// Why a free function returning http.RoundTripper instead of a
// public struct: callers compose this into an http.Client's
// Transport field, and the contract surface is just RoundTrip —
// hiding the concrete type prevents implementation drift from
// becoming a breaking change. Mirrors the
// servicekit / oneauth / OTel-Go convention for transport wrappers.
//
// This helper is one of the actionable pieces from SEP-2028
// (mcpkit issue 739). It ships independently of the upstream spec
// advancing because the W3C standards it propagates are stable and
// the user value is concrete today.
func HTTPForwardTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &httpForwardTransport{base: base}
}

type httpForwardTransport struct {
	base http.RoundTripper
}

// RoundTrip implements http.RoundTripper. See HTTPForwardTransport
// for the contract. The implementation clones the request before
// any header mutation so the caller's *http.Request is never
// touched — http.RoundTripper docs are strict: "RoundTrip must not
// modify the request, except for consuming and closing the Request's
// Body. RoundTrip may read fields of the request in a separate
// goroutine. Callers should not mutate or reuse the request until
// the Response's Body has been closed."
func (t *httpForwardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	tc := TraceContextFromContext(ctx)
	b := BaggageFromContext(ctx)

	// Determine which headers need to be added (caller-set wins).
	// Probe the original request's headers to avoid an unnecessary
	// clone when nothing would change.
	addTraceparent := tc.Traceparent != "" && req.Header.Get(HTTPHeaderTraceparent) == ""
	addTracestate := tc.Tracestate != "" && req.Header.Get(HTTPHeaderTracestate) == ""
	addBaggage := !b.IsZero() && req.Header.Get(HTTPHeaderBaggage) == ""

	if !addTraceparent && !addTracestate && !addBaggage {
		return t.base.RoundTrip(req)
	}

	cloned := req.Clone(ctx)
	if addTraceparent {
		cloned.Header.Set(HTTPHeaderTraceparent, tc.Traceparent)
	}
	if addTracestate {
		cloned.Header.Set(HTTPHeaderTracestate, tc.Tracestate)
	}
	if addBaggage {
		cloned.Header.Set(HTTPHeaderBaggage, string(b))
	}
	return t.base.RoundTrip(cloned)
}
