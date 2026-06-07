package client

// SEP-414 Phase 3 — client-side OpenTelemetry trace context propagation.
//
// Symmetric with the server-side P2 surface that landed in PR 649: when a
// non-Noop core.TracerProvider is configured via WithTracerProvider, every
// outbound Client.Call emits a span and stamps `_meta.traceparent` /
// `_meta.tracestate` onto the params; every inbound server-to-client
// request (sampling/createMessage, elicitation/create, roots/list) is
// wrapped in a span whose parent matches the inbound `_meta.traceparent`.
//
// Wiring is OFF by default — a nil or core.NoopTracerProvider{} provider
// skips the install entirely. Zero overhead on the unconfigured path.
//
// Out of scope here:
//   - SEP-2028 HTTP `traceparent` header on outbound calls (additive;
//     defer until HTTP-layer infra observability needs it).
//   - SpanKind annotations (Client / Server) — core.Span has no kind
//     surface; OTel SDK defaults to Internal for both directions.

import (
	"context"
	"encoding/json"
	"strconv"

	core "github.com/panyam/mcpkit/core"
)

// WithTracerProvider registers a core.TracerProvider that wraps every
// outbound Client.Call in a span and emits a wrap span around every
// inbound server-to-client request dispatch.
//
// nil and core.NoopTracerProvider{} are treated identically: the trace
// middleware is not installed and no `_meta.traceparent` is stamped on
// outbound params. The default (no Option set) is the same
// "tracing disabled" behavior.
//
// The trace middleware is positioned OUTERMOST in the user middleware
// chain, so user-registered ClientMiddleware (auth retry, header
// injection, custom logging) executes inside the span and contributes
// to the recorded latency. Symmetric with server.WithTracerProvider's
// outermost install.
//
// Inbound (server-to-client) request resolution:
//   - `params._meta.traceparent` is extracted via
//     core.ExtractTraceContextFromParams and attached to the handler's
//     ctx via core.WithTraceContext. Handlers consume it through
//     core.TraceContextFromContext(ctx) or pass ctx into a downstream
//     OTel-instrumented call.
//   - A wrap span named after the request method (e.g.
//     "sampling/createMessage") is emitted with the inbound trace
//     context as parent. Handler-internal spans the user emits via
//     their own tp are children of this wrap span.
//
// Outbound (Client.Call): see traceMiddleware below for the full set of
// span attributes.
func WithTracerProvider(tp core.TracerProvider) ClientOption {
	return func(c *Client) {
		c.tracerProvider = tp
	}
}

// tracingEnabled reports whether the configured TracerProvider should
// install the SEP-414 client-side trace surfaces. nil and the default
// core.NoopTracerProvider both report false so the zero-overhead path is
// preserved when no tracing is wired.
func tracingEnabled(tp core.TracerProvider) bool {
	if tp == nil {
		return false
	}
	if _, isNoop := tp.(core.NoopTracerProvider); isNoop {
		return false
	}
	return true
}

// traceMiddleware returns a ClientMiddleware that wraps every outbound
// Client.Call in a span emitted by tp. Reads the active TraceContext
// from ctx AFTER StartSpan so outbound params carry the new child span's
// traceparent (adapters like ext/otel populate ctx with the child TC on
// StartSpan; the Noop path leaves ctx unchanged, so outbound params
// carry whatever inbound TC the caller may have attached upstream).
//
// Span attributes set:
//
//   - mcp.method always
//   - mcp.tool.name on tools/call (best-effort parse)
//   - mcp.client.session.id when the client has a session
//   - mcp.error.code + RecordError when the call returns *RPCError
//
// The middleware is gated by tracingEnabled at install time, so the
// nil/Noop default never reaches this code path.
func traceMiddleware(c *Client, tp core.TracerProvider) ClientMiddleware {
	return func(ctx context.Context, method string, params any, next ClientCallFunc) (*CallResult, error) {
		attrs := make([]core.Attribute, 0, 3)
		attrs = append(attrs, core.Attribute{Key: "mcp.method", Value: method})
		if sid := c.SessionID(); sid != "" {
			attrs = append(attrs, core.Attribute{Key: "mcp.client.session.id", Value: sid})
		}
		if method == "tools/call" {
			if name := parseToolNameFromAny(params); name != "" {
				attrs = append(attrs, core.Attribute{Key: "mcp.tool.name", Value: name})
			}
		}

		ctx, span := tp.StartSpan(ctx, method, attrs...)
		defer span.End()

		outbound := core.TraceContextFromContext(ctx)
		injected := params
		if !outbound.IsZero() {
			injected = core.InjectTraceContextIntoParams(params, outbound)
		}

		res, err := next(ctx, method, injected)
		recordClientCallOutcome(span, err)
		return res, err
	}
}

// traceInboundDispatch wraps a server-to-client request dispatch in a
// span whose parent is the inbound `_meta.traceparent` (if present).
// Returns a derived context the dispatch should use so the handler sees
// the parent trace context via core.TraceContextFromContext, plus the
// Span the caller MUST end after the dispatch returns.
//
// Caller pattern (see Client.HandleServerRequestWithContext):
//
//	ctx, span := traceInboundDispatch(tp, ctx, req)
//	defer span.End()
//	// ... dispatch as usual; record outcome via recordInboundOutcome
func traceInboundDispatch(tp core.TracerProvider, ctx context.Context, req *core.Request) (context.Context, core.Span) {
	tc := core.ExtractTraceContextFromParams(req.Params)
	ctx = core.WithTraceContext(ctx, tc)
	attrs := []core.Attribute{
		{Key: "mcp.method", Value: req.Method},
	}
	return tp.StartSpan(ctx, req.Method, attrs...)
}

// recordInboundOutcome stamps mcp.error.code + RecordError on the wrap
// span when the dispatch produced a JSON-RPC error response. Non-error
// responses leave the span attribute set unchanged.
func recordInboundOutcome(span core.Span, resp *core.Response) {
	if resp == nil || resp.Error == nil {
		return
	}
	span.SetAttribute("mcp.error.code", strconv.Itoa(resp.Error.Code))
	span.RecordError(&inboundErr{code: resp.Error.Code, msg: resp.Error.Message})
}

// recordClientCallOutcome maps an outbound Call error to the span. The
// only error shape the client surfaces is *RPCError (and raw transport
// errors); both are mapped to RecordError + the structured code where
// available.
func recordClientCallOutcome(span core.Span, err error) {
	if err == nil {
		return
	}
	if rpc, ok := err.(*RPCError); ok {
		span.SetAttribute("mcp.error.code", strconv.Itoa(rpc.Code))
	}
	span.RecordError(err)
}

// inboundErr is a small error wrapper so RecordError gets a real
// error.Error() string from a JSON-RPC error response. Stays unexported
// — callers don't get a typed error from the inbound dispatch path.
type inboundErr struct {
	code int
	msg  string
}

func (e *inboundErr) Error() string { return e.msg }

// parseToolNameFromAny extracts the `name` field from a tools/call
// params payload. The client may pass params as a struct, a map, or
// any other JSON-marshalable shape, so this round-trips through JSON
// rather than asserting a concrete type. Returns "" on any decode
// failure — best-effort attribute enrichment never blocks the call.
func parseToolNameFromAny(params any) string {
	if params == nil {
		return ""
	}
	if m, ok := params.(map[string]any); ok {
		if name, _ := m["name"].(string); name != "" {
			return name
		}
		return ""
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var envelope struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return envelope.Name
}
