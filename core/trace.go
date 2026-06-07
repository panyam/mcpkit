package core

import (
	"context"
)

// SEP-414 / W3C Trace Context — contract surface.
//
// This file defines the dependency-free interfaces and propagation
// primitives needed for distributed tracing across the MCP wire. It is
// the Phase 1 deliverable from issue #312: it locks the contracts so
// downstream packages (server middleware, client outbound calls,
// experimental/ext/events cross-replica bus) can plumb a TracerProvider
// without an import cycle and without forcing an OpenTelemetry SDK
// dependency on every mcpkit consumer.
//
// What this file does NOT do:
//   - Start any spans (no transport or middleware uses TracerProvider yet —
//     that lands in P2 with server/middleware.go).
//   - Provide an OTel adapter (lands in P4 as ext/otel/, a separate go.mod).
//   - Mutate any outbound _meta envelope (P2 / P3).
//
// Spec references:
//   - SEP-414: OpenTelemetry trace context propagation for MCP
//   - W3C Trace Context: https://www.w3.org/TR/trace-context/

// MetaKeyTraceparent and MetaKeyTracestate are the keys under which W3C
// Trace Context fields are carried inside an MCP request's `_meta`
// envelope. Their values mirror the W3C HTTP header names exactly,
// without the `io.modelcontextprotocol/` namespace prefix — that prefix
// is reserved for MCP-defined fields (see core/stateless.go), and
// `traceparent` / `tracestate` are W3C-defined.
const (
	MetaKeyTraceparent = "traceparent"
	MetaKeyTracestate  = "tracestate"
)

// TraceContext is the propagated W3C Trace Context for a single MCP
// request. Both fields are opaque strings — this package validates the
// `traceparent` field's structural form (W3C version-00) but does not
// parse the trace-id or span-id; an adapter (e.g., ext/otel) consumes
// the raw strings and feeds them to the OTel propagator.
//
// A zero TraceContext (both fields empty) means "no trace context is
// active." Callers must treat it as a valid absence, not an error.
type TraceContext struct {
	// Traceparent is the W3C `traceparent` value as it appears on the
	// wire: e.g. `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`.
	// Empty when no trace context is propagated.
	Traceparent string

	// Tracestate is the W3C `tracestate` value (vendor-specific list).
	// May be empty even when Traceparent is set. Opaque to mcpkit core.
	Tracestate string
}

// IsZero reports whether tc carries no trace context at all. Equivalent
// to `tc == TraceContext{}`; provided so call sites read as a single
// branch ("if tc.IsZero() { ... }").
func (tc TraceContext) IsZero() bool {
	return tc.Traceparent == "" && tc.Tracestate == ""
}

// TracerProvider is the minimal tracing seam mcpkit components consume.
// It is intentionally smaller than the full go.opentelemetry.io/otel
// `trace.TracerProvider` so the base module can stay dep-free; the
// experimental/ext/otel adapter (P4) implements this interface on top of
// the real OTel SDK.
//
// Implementations MUST:
//   - Be safe for concurrent use by multiple goroutines.
//   - Return a non-nil Span even when the provider is a no-op — callers
//     do not nil-check the returned Span.
//   - Honor the parent trace context carried in ctx (extracted via
//     TraceContextFromContext or by an adapter-specific propagator).
//
// The default implementation, NoopTracerProvider, performs no allocation
// and is safe to embed in tests and in production wiring where tracing
// is disabled.
type TracerProvider interface {
	// StartSpan begins a new span named `name` and returns a derived
	// context that carries the span as the parent for any nested
	// StartSpan calls. The returned Span MUST have its End method called
	// exactly once when the span's work is done.
	StartSpan(ctx context.Context, name string, attrs ...Attribute) (context.Context, Span)
}

// Span is a single in-flight tracing span. Implementations MUST be safe
// for concurrent SetAttribute / RecordError calls; End MUST be called
// exactly once. After End returns, subsequent SetAttribute / RecordError
// calls are no-ops (implementations may log a warning but MUST NOT
// panic).
type Span interface {
	// End closes the span and emits it to the configured exporter.
	// Subsequent calls are no-ops.
	End()

	// SetAttribute records a string-valued attribute on the span. Keys
	// follow OpenTelemetry semantic conventions where applicable
	// (e.g. `mcp.method`, `mcp.session.id`). Numeric or bool attributes
	// are out of scope for the P1 interface — callers stringify if
	// needed; a richer typed attribute interface may land in a future
	// version once a real adapter requires it.
	SetAttribute(k, v string)

	// RecordError attaches an error to the span. The adapter decides
	// the exact mapping (e.g. OTel records an event with name
	// `exception` and the error message as the `exception.message`
	// attribute). Passing nil is a no-op.
	RecordError(err error)
}

// Attribute is a single key/value pair attached to a span. Both fields
// are strings to keep the contract surface dep-free; see Span.SetAttribute
// for the rationale.
type Attribute struct {
	Key   string
	Value string
}

// NoopTracerProvider is the default TracerProvider used when no tracing
// is configured. StartSpan returns the input context unchanged plus a
// noopSpan whose methods do nothing. Zero allocations on the hot path.
type NoopTracerProvider struct{}

// StartSpan returns ctx unchanged and a no-op Span.
func (NoopTracerProvider) StartSpan(ctx context.Context, _ string, _ ...Attribute) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) End()                     {}
func (noopSpan) SetAttribute(_, _ string) {}
func (noopSpan) RecordError(_ error)      {}

// --- W3C Trace Context extraction / injection --------------------------------

// ExtractTraceContext reads the W3C `traceparent` and `tracestate`
// fields from an MCP `_meta` map. It returns a zero TraceContext when
// the keys are absent, when the values are not strings, or when the
// traceparent value fails W3C structural validation (wrong length,
// non-hex characters, all-zero trace-id or parent-id, or an unknown
// version byte).
//
// Lenient values are not retained: a malformed traceparent yields an
// empty Traceparent AND an empty Tracestate, because per W3C
// recommendation a vendor MUST NOT forward a tracestate it cannot
// associate with a valid traceparent.
func ExtractTraceContext(meta map[string]any) TraceContext {
	if meta == nil {
		return TraceContext{}
	}
	tp, _ := meta[MetaKeyTraceparent].(string)
	ts, _ := meta[MetaKeyTracestate].(string)
	if !isValidTraceparent(tp) {
		return TraceContext{}
	}
	return TraceContext{Traceparent: tp, Tracestate: ts}
}

// InjectTraceContext writes the W3C `traceparent` and `tracestate`
// fields into an MCP `_meta` map. Empty fields on tc are NOT written —
// `_meta` stays clean. A nil meta panics; callers MUST provide a
// non-nil map (mirrors the standard library's `http.Header.Set`
// expectations).
//
// Idempotent: calling InjectTraceContext twice with the same tc leaves
// meta in the same end state.
func InjectTraceContext(meta map[string]any, tc TraceContext) {
	if tc.Traceparent != "" {
		meta[MetaKeyTraceparent] = tc.Traceparent
	}
	if tc.Tracestate != "" {
		meta[MetaKeyTracestate] = tc.Tracestate
	}
}

// isValidTraceparent enforces the W3C version-00 structural form:
// `00-<32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags>` with all
// lowercase hex. Trace-id and span-id MUST NOT be all-zero (W3C
// requires non-zero IDs for valid contexts).
//
// Versions above `00` are rejected for now. When a future W3C version
// publishes a structural form we want to honor, this function can
// branch on the version byte.
func isValidTraceparent(s string) bool {
	if len(s) != 55 {
		return false
	}
	// Layout: 00 - tid(32) - sid(16) - flg(2)
	if s[2] != '-' || s[35] != '-' || s[52] != '-' {
		return false
	}
	version := s[0:2]
	traceID := s[3:35]
	spanID := s[36:52]
	flags := s[53:55]
	if version != "00" {
		return false
	}
	if !isLowerHex(traceID) || isAllZero(traceID) {
		return false
	}
	if !isLowerHex(spanID) || isAllZero(spanID) {
		return false
	}
	if !isLowerHex(flags) {
		return false
	}
	return true
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func isAllZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// --- context.Context plumbing ------------------------------------------------

type traceContextCtxKey struct{}

// WithTraceContext returns a derived context carrying tc. The dispatch
// layer (P2) calls this after extracting the inbound `_meta.traceparent`
// / `_meta.tracestate` fields so handlers can read the active trace
// context via BaseContext.TraceContext() or TraceContextFromContext.
//
// A zero tc is stored as-is so that downstream calls observe an
// explicit "tracing disabled for this request" signal rather than
// falling through to whatever ctx may have carried before. Use this to
// scrub an inherited trace context at a boundary.
func WithTraceContext(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, traceContextCtxKey{}, tc)
}

// TraceContextFromContext returns the TraceContext carried by ctx, or
// a zero TraceContext when none has been attached. Always safe to call;
// never panics. Use TraceContext.IsZero to test for absence.
func TraceContextFromContext(ctx context.Context) TraceContext {
	tc, _ := ctx.Value(traceContextCtxKey{}).(TraceContext)
	return tc
}
