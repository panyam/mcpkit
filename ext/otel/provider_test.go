package otel_test

// SEP-414 Phase 4 — ext/otel adapter tests. Drives the adapter through a
// real go.opentelemetry.io/otel/sdk pipeline so assertions read back actual
// sdktrace.ReadOnlySpan instances. No fake provider — the SDK is the spec.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	core "github.com/panyam/mcpkit/core"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newRecordingProvider returns a Provider backed by a fresh sdktrace
// TracerProvider with an InMemoryExporter attached via a sync span
// processor. Tests pull span snapshots from the exporter after dispatching.
func newRecordingProvider(t *testing.T, opts ...mcpotel.Option) (*mcpotel.Provider, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	sdkTP := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() {
		_ = sdkTP.Shutdown(context.Background())
	})
	return mcpotel.NewProvider(sdkTP, opts...), exp
}

// TestNewProvider_NilOtelTP_Panics verifies the fail-fast on nil. A silent
// no-op would lose spans without surfacing the misconfiguration; panic
// makes the wiring error visible at server bootstrap.
func TestNewProvider_NilOtelTP_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = mcpotel.NewProvider(nil)
	})
}

// TestStartSpan_EmitsSpanWithName confirms the span name passes through
// unchanged and the inbound ctx attributes show up on the recorded span.
func TestStartSpan_EmitsSpanWithName(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "tools/call",
		core.Attribute{Key: "mcp.method", Value: "tools/call"},
		core.Attribute{Key: "mcp.tool.name", Value: "echo"},
	)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "tools/call", spans[0].Name)
	attrs := attrMap(spans[0].Attributes)
	assert.Equal(t, "tools/call", attrs["mcp.method"])
	assert.Equal(t, "echo", attrs["mcp.tool.name"])
}

// TestSpan_SetAttribute_AfterStart verifies that SetAttribute lands on the
// exported span just like attributes passed to StartSpan.
func TestSpan_SetAttribute_AfterStart(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "tools/call")
	span.SetAttribute("mcp.error.code", "-32601")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "-32601", attrMap(spans[0].Attributes)["mcp.error.code"])
}

// TestSpan_RecordError_SetsStatusAndEvent verifies the OTel idiom: an
// error is BOTH recorded as a span event (visible in trace tooling) AND
// degrades the span status to codes.Error (filters / counts).
func TestSpan_RecordError_SetsStatusAndEvent(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "tools/call")
	span.RecordError(errors.New("boom"))
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	assert.Equal(t, "boom", spans[0].Status.Description)
	require.NotEmpty(t, spans[0].Events)
	assert.Equal(t, "exception", spans[0].Events[0].Name)
}

// TestSpan_RecordError_NilNoop pins the contract that nil error never
// records a status downgrade — handlers that always call RecordError
// (defensive style) must not paint every success span as errored.
func TestSpan_RecordError_NilNoop(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "tools/call")
	span.RecordError(nil)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.NotEqual(t, codes.Error, spans[0].Status.Code)
	assert.Empty(t, spans[0].Events)
}

// TestSpan_End_Idempotent verifies the core.Span contract that End is a
// one-shot. The CAS guard in Span.End prevents the second End from
// reaching the SDK's noisy "span already ended" warning path AND
// guarantees only one ReadOnlySpan is exported.
func TestSpan_End_Idempotent(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "tools/call")
	span.End()
	span.End()
	span.End()

	assert.Len(t, exp.GetSpans(), 1)
}

// TestStartSpan_InboundTraceparent_BecomesParent verifies the SEP-414 P2
// inbound path: a TraceContext attached to ctx via core.WithTraceContext
// (typically by the server's traceMiddleware after _meta extraction or
// the HTTP header bridge) is parsed into an OTel SpanContext and
// installed as the new span's parent.
func TestStartSpan_InboundTraceparent_BecomesParent(t *testing.T) {
	p, exp := newRecordingProvider(t)
	parentTID := "0af7651916cd43dd8448eb211c80319c"
	parentSID := "b7ad6b7169203331"
	tc := core.TraceContext{
		Traceparent: "00-" + parentTID + "-" + parentSID + "-01",
	}
	ctx := core.WithTraceContext(context.Background(), tc)
	_, span := p.StartSpan(ctx, "tools/call")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, parentTID, spans[0].SpanContext.TraceID().String(),
		"child span should inherit the parent's trace ID")
	assert.Equal(t, parentSID, spans[0].Parent.SpanID().String(),
		"recorded Parent should match the inbound spanID")
	assert.True(t, spans[0].Parent.IsRemote(), "inbound parent must be flagged Remote")
}

// TestStartSpan_OutboundContextSync_ReflectsChildSpan is the core
// guarantee that drives SEP-414 P2's outbound _meta injection: after
// StartSpan returns, core.TraceContextFromContext(ctx) carries the NEW
// child span's traceparent, not the inbound parent's. Without this sync
// every outbound notification would carry the inbound parent ID, which
// would be wrong (the child span did the work; the next hop should
// chain from there).
func TestStartSpan_OutboundContextSync_ReflectsChildSpan(t *testing.T) {
	p, exp := newRecordingProvider(t)
	parentTID := "0af7651916cd43dd8448eb211c80319c"
	parentSID := "b7ad6b7169203331"
	inbound := core.TraceContext{Traceparent: "00-" + parentTID + "-" + parentSID + "-01"}
	ctx := core.WithTraceContext(context.Background(), inbound)

	childCtx, span := p.StartSpan(ctx, "tools/call")
	outbound := core.TraceContextFromContext(childCtx)
	span.End()

	require.False(t, outbound.IsZero(), "child ctx must carry a TraceContext after StartSpan")
	assert.Contains(t, outbound.Traceparent, parentTID,
		"outbound traceparent should share the trace ID with the inbound parent")
	assert.NotEqual(t, inbound.Traceparent, outbound.Traceparent,
		"outbound traceparent must reflect the child span, not the inbound parent")

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	expected := "00-" + spans[0].SpanContext.TraceID().String() + "-" + spans[0].SpanContext.SpanID().String() + "-" + spans[0].SpanContext.TraceFlags().String()
	assert.Equal(t, expected, outbound.Traceparent,
		"outbound traceparent should equal the recorded child SpanContext")
}

// TestStartSpan_NoInboundParent_StartsFreshTrace verifies the cold-start
// path: a request with no _meta.traceparent and no HTTP header bridge
// produces a span with a fresh trace ID and no recorded parent. The OTel
// SDK assigns the trace/span IDs; we just confirm we don't accidentally
// install a zero-valued parent.
func TestStartSpan_NoInboundParent_StartsFreshTrace(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "tools/call")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.False(t, spans[0].Parent.IsValid(), "no inbound parent should leave Parent unset")
	assert.True(t, spans[0].SpanContext.IsValid())
}

// TestNewProvider_WithInstrumentationName tags emitted spans with the
// supplied instrumentation library name. Observability backends use this
// as the per-library grouping dimension; verifying that the override
// reaches the SDK protects against the default leaking when callers
// explicitly customize.
func TestNewProvider_WithInstrumentationName(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	sdkTP := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = sdkTP.Shutdown(context.Background()) })

	p := mcpotel.NewProvider(sdkTP, mcpotel.WithInstrumentationName("custom-instr"))
	_, span := p.StartSpan(context.Background(), "ping")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "custom-instr", spans[0].InstrumentationScope.Name)
}

// TestEndToEnd_ServerWithTracerProvider exercises the full P2+P4 surface:
// a real server with the adapter wired via server.WithTracerProvider
// receives a tools/call carrying an inbound _meta.traceparent, dispatches
// through the trace middleware, and the SDK exporter records exactly one
// span tagged with mcp.method=tools/call, mcp.tool.name=echo, and a
// parent that matches the inbound traceparent. This is the interop test
// — if it passes, the wire is end-to-end propagating real traces.
func TestEndToEnd_ServerWithTracerProvider(t *testing.T) {
	p, exp := newRecordingProvider(t)

	srv := server.NewServer(core.ServerInfo{Name: "e2e", Version: "1.0"},
		server.WithTracerProvider(p))

	srv.RegisterTool(core.ToolDef{Name: "echo"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("ok"), nil
		})

	// Initialize via dispatch — Server.Dispatch routes the JSON-RPC
	// initialize handshake before any tools/call is accepted.
	initParams := `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1.0"}}`
	_, err := srv.Dispatch(context.Background(), &core.Request{
		ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(initParams),
	})
	require.NoError(t, err)
	_, _ = srv.Dispatch(context.Background(), &core.Request{Method: "notifications/initialized"})

	parentTID := "0af7651916cd43dd8448eb211c80319c"
	parentSID := "b7ad6b7169203331"
	toolParams := `{"name":"echo","_meta":{"traceparent":"00-` + parentTID + `-` + parentSID + `-01"}}`
	_, err = srv.Dispatch(context.Background(), &core.Request{
		ID: json.RawMessage(`2`), Method: "tools/call",
		Params: json.RawMessage(toolParams),
	})
	require.NoError(t, err)

	spans := exp.GetSpans()
	// initialize and notifications/initialized may or may not be wrapped
	// depending on the dispatch path; only the tools/call span is the
	// contract. Find it by name.
	var toolSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "tools/call" {
			toolSpan = &spans[i]
			break
		}
	}
	require.NotNil(t, toolSpan, "tools/call span must be exported")
	attrs := attrMap(toolSpan.Attributes)
	assert.Equal(t, "tools/call", attrs["mcp.method"])
	assert.Equal(t, "echo", attrs["mcp.tool.name"])
	assert.Equal(t, parentTID, toolSpan.SpanContext.TraceID().String(),
		"tools/call span should inherit the inbound trace ID")
	assert.Equal(t, parentSID, toolSpan.Parent.SpanID().String(),
		"tools/call span Parent should match the inbound _meta.traceparent")
}

// attrMap flattens an OTel attribute slice into a string-keyed map for
// concise lookups in assertions. core.Attribute is always string/string,
// so AsString() is lossless here.
func attrMap(kvs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.AsString()
	}
	return out
}

// --- P6 active-span accessor (issue 661) ------------------------------------

func TestSpanFromContext_AdapterPublishesSpan(t *testing.T) {
	p, exp := newRecordingProvider(t)
	ctx, started := p.StartSpan(context.Background(), "tools/call")
	got := core.SpanFromContext(ctx)
	require.NotNil(t, got, "SpanFromContext must never return nil")
	assert.Same(t, started, got,
		"core.SpanFromContext(ctx) should return the same Span the adapter just returned from StartSpan")

	got.SetAttribute("mcp.auth.principal", "alice")
	started.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	attrs := attrMap(spans[0].Attributes)
	assert.Equal(t, "alice", attrs["mcp.auth.principal"],
		"attribute set via SpanFromContext must land on the recorded span — proves the enrichment pattern works against a real exporter")
}

func TestSpanFromContext_NestedAdapterStartSpan_InnermostWins(t *testing.T) {
	p, exp := newRecordingProvider(t)
	outerCtx, outerSpan := p.StartSpan(context.Background(), "outer")
	innerCtx, innerSpan := p.StartSpan(outerCtx, "inner")

	assert.Same(t, innerSpan, core.SpanFromContext(innerCtx),
		"inner ctx should yield the inner span")
	assert.Same(t, outerSpan, core.SpanFromContext(outerCtx),
		"outer ctx should still yield the outer span after inner StartSpan")

	innerSpan.End()
	outerSpan.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 2)
}
