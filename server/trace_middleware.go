package server

// SEP-414 Phase 2 — server-side OpenTelemetry trace context propagation.
//
// This file holds the internal middleware and helpers that take the
// dependency-free TracerProvider contract from core (PR 644) and wire it
// into the dispatch path. Wiring is gated on server.WithTracerProvider —
// when no provider is configured (or NoopTracerProvider is in use), this
// middleware is not installed and there is zero runtime cost.
//
// Out of scope here:
//   - Outbound child-span emission around server-to-client requests
//     (P3 client-side spans).
//   - The OTel SDK adapter that implements core.TracerProvider on top of
//     go.opentelemetry.io/otel (P4, lands as a separate ext/otel/ module).
//   - HTTP traceparent header → _meta bridging — lives in the streamable
//     transport (SEP-2028) and is independent of this middleware; it
//     attaches the inbound TraceContext via core.WithTraceContext so the
//     middleware's fallback path picks it up.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	core "github.com/panyam/mcpkit/core"
)

// SEP-2028 — W3C Trace Context HTTP headers. The canonical W3C names are
// lowercase (`traceparent` / `tracestate`); Go's net/http canonicalizes
// header keys to title-case, but http.Header.Get is case-insensitive, so
// these constants serve as documentation of the spec name and a single
// edit point if a future version renames them.
const (
	httpHeaderTraceparent = "Traceparent"
	httpHeaderTracestate  = "Tracestate"
)

// withTraceContextFromHTTPHeaders bridges the W3C `traceparent` /
// `tracestate` HTTP headers (SEP-2028) into ctx via core.WithTraceContext.
// Returns ctx unchanged when the traceparent header is absent or its
// value fails W3C structural validation; tracestate without traceparent
// is silently dropped per the same W3C rule that ExtractTraceContext
// enforces.
//
// The trace middleware consults this out-of-band context only when the
// inbound `params._meta.traceparent` is absent — explicit in-band trace
// context always wins. SEP-2028 frames the HTTP header as a vehicle for
// `_meta`; we honor the semantic without rewriting the request body.
func withTraceContextFromHTTPHeaders(ctx context.Context, h http.Header) context.Context {
	tp := h.Get(httpHeaderTraceparent)
	if tp == "" {
		return ctx
	}
	tc := core.ExtractTraceContext(map[string]any{
		core.MetaKeyTraceparent: tp,
		core.MetaKeyTracestate:  h.Get(httpHeaderTracestate),
	})
	if tc.IsZero() {
		return ctx
	}
	return core.WithTraceContext(ctx, tc)
}

// WithTracerProvider registers a core.TracerProvider that wraps every
// dispatched JSON-RPC request in an inbound span and propagates the W3C
// Trace Context on outbound notifications and server-to-client requests.
//
// When nil or core.NoopTracerProvider{} is passed, no trace middleware is
// installed and no outbound _meta injection occurs — the server stays on
// the zero-overhead default. The default (no Option set) is the same
// "tracing disabled" behavior.
//
// The trace middleware is positioned OUTERMOST in the user middleware
// chain, so user-registered middleware (rate limit, audit, custom auth)
// executes inside the span and contributes to the recorded latency.
// Outbound _meta injection (traceparent/tracestate) sits OUTSIDE any
// user-registered NotifyInterceptor / RequestInterceptor, so user
// interceptors observe the same wire form the client will receive —
// useful for audit logs that want the full envelope.
//
// Inbound trace context resolution order:
//  1. params._meta.traceparent / params._meta.tracestate (in-band).
//  2. The TraceContext attached to ctx by the transport (e.g., the
//     streamable HTTP transport bridges the HTTP `traceparent` header
//     into ctx per SEP-2028).
//
// In-band wins over out-of-band. Malformed traceparent values are
// dropped per W3C ("MUST NOT forward"); the span still emits with no
// parent.
//
// Adapters (P4 ext/otel) MAY update the active TraceContext in ctx after
// StartSpan so outbound _meta carries the new child span's traceparent.
// The Noop default leaves ctx unchanged, so outbound _meta carries the
// inbound traceparent verbatim — still correlates within the trace, just
// without a finer parent-child link at the next hop. P4 will tighten
// this for OTel users.
func WithTracerProvider(tp core.TracerProvider) Option {
	return func(o *serverOptions) {
		o.tracerProvider = tp
	}
}

// traceMiddleware returns a Middleware that wraps every JSON-RPC dispatch
// in a span emitted by tp. The middleware:
//
//   - Extracts the inbound W3C Trace Context from `params._meta`
//     (preferred) or from ctx (HTTP-header bridge); attaches the result
//     via core.WithTraceContext so handlers can read it via
//     ctx.TraceContext().
//   - Calls tp.StartSpan with the JSON-RPC method as the span name and
//     `mcp.method`, `mcp.session.id`, and `mcp.tool.name` (tools/call
//     only) as attributes.
//   - Wraps the session's NotifyFunc and RequestFunc so every outbound
//     server-to-client message carries `_meta.traceparent` /
//     `_meta.tracestate` derived from the active span's TraceContext.
//   - Records `mcp.error.code` + RecordError on JSON-RPC errors, and
//     `mcp.tool.is_error="true"` on `tools/call` results with isError.
//   - Ends the span exactly once on return.
//
// Caller must hold tp != nil; the dispatch wiring layer guarantees this
// by installing the middleware only when WithTracerProvider received a
// non-Noop provider.
func traceMiddleware(tp core.TracerProvider) Middleware {
	return func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		tc := core.ExtractTraceContextFromParams(req.Params)
		if tc.IsZero() {
			tc = core.TraceContextFromContext(ctx)
		}
		ctx = core.WithTraceContext(ctx, tc)

		attrs := make([]core.Attribute, 0, 3)
		if req.Method != "" {
			attrs = append(attrs, core.Attribute{Key: "mcp.method", Value: req.Method})
		}
		if sid := core.GetSessionID(ctx); sid != "" {
			attrs = append(attrs, core.Attribute{Key: "mcp.session.id", Value: sid})
		}
		if req.Method == "tools/call" {
			if name := parseToolCallName(req.Params); name != "" {
				attrs = append(attrs, core.Attribute{Key: "mcp.tool.name", Value: name})
			}
		}

		spanName := req.Method
		if spanName == "" {
			spanName = "mcp.request"
		}
		ctx, span := tp.StartSpan(ctx, spanName, attrs...)
		defer span.End()

		// Adapters may update the trace context in ctx during StartSpan
		// to reflect the newly-created child span. Re-read so outbound
		// _meta carries the child traceparent when available; falls
		// back to the inbound TraceContext otherwise.
		outbound := core.TraceContextFromContext(ctx)
		if !outbound.IsZero() {
			ctx = core.WrapSessionNotifyFunc(ctx, traceInjectNotifyWrap(outbound))
			ctx = core.WrapSessionRequestFunc(ctx, traceInjectRequestWrap(outbound))
		}

		resp, err := next(ctx, req)
		recordSpanOutcome(span, resp, err)
		return resp, err
	}
}

// traceInjectNotifyWrap returns a NotifyFunc wrapper that injects tc into
// outbound `_meta.traceparent` / `_meta.tracestate` before forwarding to
// the next NotifyFunc in the chain. Existing `_meta.traceparent` set
// explicitly by the handler wins — the wrap never clobbers.
func traceInjectNotifyWrap(tc core.TraceContext) func(core.NotifyFunc) core.NotifyFunc {
	return func(orig core.NotifyFunc) core.NotifyFunc {
		return func(method string, params any) {
			orig(method, core.InjectTraceContextIntoParams(params, tc))
		}
	}
}

// traceInjectRequestWrap returns a RequestFunc wrapper that injects tc into
// outbound server-to-client request params. Sibling to traceInjectNotifyWrap.
func traceInjectRequestWrap(tc core.TraceContext) func(core.RequestFunc) core.RequestFunc {
	return func(orig core.RequestFunc) core.RequestFunc {
		return func(ctx context.Context, method string, params any) (json.RawMessage, error) {
			return orig(ctx, method, core.InjectTraceContextIntoParams(params, tc))
		}
	}
}

// parseToolCallName extracts the `name` field from a tools/call params
// envelope. Returns "" on any decode failure — best-effort attribute
// enrichment never blocks dispatch.
func parseToolCallName(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var envelope struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return ""
	}
	return envelope.Name
}

// recordSpanOutcome stamps error / tool-error attributes on the span before
// it ends. Three layers, in priority order:
//
//   - transport-level error (err != nil): RecordError only. The error
//     surfaces to the transport, which maps it to an HTTP-level response.
//   - JSON-RPC protocol error (resp.Error != nil): SetAttribute
//     `mcp.error.code` and RecordError with the error message.
//   - tools/call in-stream error (resp.Result is a ToolResult with
//     IsError): SetAttribute `mcp.tool.is_error="true"`.
//
// Non-error responses leave the span attribute set unchanged.
func recordSpanOutcome(span core.Span, resp *core.Response, err error) {
	if err != nil {
		span.RecordError(err)
		return
	}
	if resp == nil {
		return
	}
	if resp.Error != nil {
		span.SetAttribute("mcp.error.code", strconv.Itoa(resp.Error.Code))
		span.RecordError(errors.New(resp.Error.Message))
		return
	}
	if isToolErrorResult(resp.Result) {
		span.SetAttribute("mcp.tool.is_error", "true")
	}
}

// isToolErrorResult reports whether result is a tools/call ToolResult
// carrying IsError == true. The dispatch layer assigns the concrete
// core.ToolResult struct on the success path, so a fast type-switch
// covers the common case; the JSON round-trip is the conservative
// fallback for unusual handler return types (custom JSON-marshalable
// shapes routed through HandleMethod, etc.).
func isToolErrorResult(result any) bool {
	if result == nil {
		return false
	}
	if tr, ok := result.(core.ToolResult); ok {
		return tr.IsError
	}
	if tr, ok := result.(*core.ToolResult); ok && tr != nil {
		return tr.IsError
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return false
	}
	var probe struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.IsError
}
