package otel

import (
	"encoding/hex"

	core "github.com/panyam/mcpkit/core"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// traceContextToSpanContext parses a W3C version-00 traceparent (already
// validated by core.ExtractTraceContext to be structurally correct) into
// an OTel trace.SpanContext suitable as a remote parent. The tracestate
// is parsed via OTel's standard ParseTraceState — invalid tracestate
// drops to empty (matches OTel's permissive behavior; we don't fail the
// span because of vendor-side noise).
//
// Returns (zero, false) when:
//   - the traceparent is empty (the tc.IsZero() caller path);
//   - the traceparent fails OTel's stricter validation (TraceID /
//     SpanID parsing). core.ExtractTraceContext already enforces W3C
//     version-00 structure, so this is a belt-and-braces guard against
//     someone constructing TraceContext{} manually with a hand-crafted
//     string.
func traceContextToSpanContext(tc core.TraceContext) (oteltrace.SpanContext, bool) {
	if tc.Traceparent == "" {
		return oteltrace.SpanContext{}, false
	}
	// Layout: 00-<32-hex-trace-id>-<16-hex-span-id>-<2-hex-flags>
	if len(tc.Traceparent) != 55 {
		return oteltrace.SpanContext{}, false
	}
	traceIDHex := tc.Traceparent[3:35]
	spanIDHex := tc.Traceparent[36:52]
	flagsHex := tc.Traceparent[53:55]

	traceID, err := oteltrace.TraceIDFromHex(traceIDHex)
	if err != nil {
		return oteltrace.SpanContext{}, false
	}
	spanID, err := oteltrace.SpanIDFromHex(spanIDHex)
	if err != nil {
		return oteltrace.SpanContext{}, false
	}
	flagsBytes, err := hex.DecodeString(flagsHex)
	if err != nil || len(flagsBytes) != 1 {
		return oteltrace.SpanContext{}, false
	}
	var tracestate oteltrace.TraceState
	if tc.Tracestate != "" {
		if ts, err := oteltrace.ParseTraceState(tc.Tracestate); err == nil {
			tracestate = ts
		}
	}
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.TraceFlags(flagsBytes[0]),
		TraceState: tracestate,
		Remote:     true,
	})
	if !sc.IsValid() {
		return oteltrace.SpanContext{}, false
	}
	return sc, true
}

// spanContextToTraceContext formats an OTel SpanContext into a W3C
// version-00 traceparent + tracestate pair. Used to update ctx via
// core.WithTraceContext after StartSpan so the SEP-414 P2 outbound
// _meta injection wraps stamp the wire with the new child span's
// traceparent.
//
// Returns a zero core.TraceContext when sc is invalid (e.g., the
// returned span from a noop tracer). Callers branch on tc.IsZero().
func spanContextToTraceContext(sc oteltrace.SpanContext) core.TraceContext {
	if !sc.IsValid() {
		return core.TraceContext{}
	}
	traceparent := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + hex.EncodeToString([]byte{byte(sc.TraceFlags())})
	return core.TraceContext{
		Traceparent: traceparent,
		Tracestate:  sc.TraceState().String(),
	}
}
