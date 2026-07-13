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
		Params: core.NewRawJSON(json.RawMessage(initParams)),
	})
	require.NoError(t, err)
	_, _ = srv.Dispatch(context.Background(), &core.Request{Method: "notifications/initialized"})

	parentTID := "0af7651916cd43dd8448eb211c80319c"
	parentSID := "b7ad6b7169203331"
	toolParams := `{"name":"echo","_meta":{"traceparent":"00-` + parentTID + `-` + parentSID + `-01"}}`
	_, err = srv.Dispatch(context.Background(), &core.Request{
		ID: json.RawMessage(`2`), Method: "tools/call",
		Params: core.NewRawJSON(json.RawMessage(toolParams)),
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

// --- P6 span links (issue 662) ----------------------------------------------

const (
	linkTP1 = "00-4bf92f3577b34da6a3ce929d0e0e4736-1111111111111111-01"
	linkTP2 = "00-aabbccddeeff00112233445566778899-2222222222222222-01"
)

func TestProvider_StartSpanLinked_EmitsOTelLinks(t *testing.T) {
	p, exp := newRecordingProvider(t)
	links := []core.Link{
		core.LinkFromTraceContext(core.TraceContext{Traceparent: linkTP1}),
		{
			TraceContext: core.TraceContext{Traceparent: linkTP2},
			Attributes:   []core.Attribute{{Key: "link.kind", Value: "sibling-task"}},
		},
	}
	_, span := p.StartSpanLinked(context.Background(), "task.execute", links,
		core.Attribute{Key: "mcp.task.id", Value: "t-123"},
	)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	require.Len(t, spans[0].Links, 2)

	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", spans[0].Links[0].SpanContext.TraceID().String())
	assert.Empty(t, spans[0].Links[0].Attributes, "first link has no per-link attributes")

	assert.Equal(t, "aabbccddeeff00112233445566778899", spans[0].Links[1].SpanContext.TraceID().String())
	require.NotEmpty(t, spans[0].Links[1].Attributes)
	assert.Equal(t, "sibling-task", spans[0].Links[1].Attributes[0].Value.AsString(),
		"per-link Attributes must flow through to the OTel link")
}

func TestProvider_StartSpanLinked_FiltersInvalid(t *testing.T) {
	p, exp := newRecordingProvider(t)
	links := []core.Link{
		{TraceContext: core.TraceContext{}},                                       // zero — dropped
		{TraceContext: core.TraceContext{Traceparent: "not-a-valid-traceparent"}}, // malformed — dropped
		core.LinkFromTraceContext(core.TraceContext{Traceparent: linkTP1}),        // valid — kept
	}
	_, span := p.StartSpanLinked(context.Background(), "filtered", links)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	require.Len(t, spans[0].Links, 1, "invalid links must be silently dropped before the OTel call")
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", spans[0].Links[0].SpanContext.TraceID().String())
}

func TestProvider_StartSpanLinked_EmptyLinks_BehavesAsStartSpan(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpanLinked(context.Background(), "empty-links", nil)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Empty(t, spans[0].Links)
}

func TestProvider_SpanAddLink_LandsBeforeEnd(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "addlink-test")

	span.AddLink(core.Link{
		TraceContext: core.TraceContext{Traceparent: linkTP1},
		Attributes:   []core.Attribute{{Key: "link.kind", Value: "discovered-mid-flight"}},
	})
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	require.Len(t, spans[0].Links, 1, "AddLink before End must land on the exported span")
	assert.Equal(t, "discovered-mid-flight", spans[0].Links[0].Attributes[0].Value.AsString())
}

func TestProvider_SpanAddLink_AfterEnd_IsNoOp(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "addlink-after-end")
	span.End()
	span.AddLink(core.Link{TraceContext: core.TraceContext{Traceparent: linkTP1}})

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Empty(t, spans[0].Links, "AddLink after End must be a no-op (CAS-guarded wrapper)")
}

func TestProvider_SpanAddLink_InvalidLink_Dropped(t *testing.T) {
	p, exp := newRecordingProvider(t)
	_, span := p.StartSpan(context.Background(), "addlink-invalid")
	span.AddLink(core.Link{TraceContext: core.TraceContext{}})                       // zero
	span.AddLink(core.Link{TraceContext: core.TraceContext{Traceparent: "garbage"}}) // malformed
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Empty(t, spans[0].Links, "invalid AddLink calls must be silently dropped")
}

// --- P6 ext/tasks complement: WithNewRootSpan (issue 659) -------------------

// When ctx carries an inbound trace context, StartSpan parents the new
// span under it (default behavior, regression-guards the existing
// adapter contract).
func TestStartSpan_InheritsParentFromTraceContext(t *testing.T) {
	p, exp := newRecordingProvider(t)
	parentTC := core.TraceContext{Traceparent: linkTP1}
	ctx := core.WithTraceContext(context.Background(), parentTC)

	_, span := p.StartSpan(ctx, "child")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", spans[0].SpanContext.TraceID().String(),
		"child span must share the inbound trace ID — the adapter's default parent-inheritance path")
}

// When ctx is marked via core.WithNewRootSpan, the adapter MUST strip
// any inherited parent (both the core.TraceContext and any latent OTel
// span context already installed) so the new span emits as a fresh
// root trace. This is the ext/otel-side enabler for the ext/tasks
// task.execute root + link pattern (issue 659).
func TestStartSpan_NewRootSpan_DetachesFromCoreTraceContext(t *testing.T) {
	p, exp := newRecordingProvider(t)
	parentTC := core.TraceContext{Traceparent: linkTP1}
	ctx := core.WithTraceContext(context.Background(), parentTC)
	ctx = core.WithNewRootSpan(ctx)

	_, span := p.StartSpan(ctx, "task.execute")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.NotEqual(t, "4bf92f3577b34da6a3ce929d0e0e4736", spans[0].SpanContext.TraceID().String(),
		"WithNewRootSpan must scrub the inherited core.TraceContext so the new span emits its own root trace ID")
	assert.False(t, spans[0].Parent.IsValid(),
		"new-root span must have no parent SpanContext — observability backends render it as a top-level trace")
}

// When ctx already carries an OTel parent installed by an earlier
// StartSpan, WithNewRootSpan must strip THAT too — not just the
// core.TraceContext. This is the exact shape goroutine bgCtx ends up
// in after core.DetachForBackground (context.WithoutCancel preserves
// every value, including the OTel span context, so a subsequent
// StartSpan would otherwise silently re-parent under the dispatch
// span).
func TestStartSpan_NewRootSpan_DetachesFromOTelParent(t *testing.T) {
	p, exp := newRecordingProvider(t)
	parentCtx, parentSpan := p.StartSpan(context.Background(), "create")
	parentTraceID := parentSpan.(*mcpotel.Span)
	_ = parentTraceID
	defer parentSpan.End()

	rootCtx := core.WithNewRootSpan(parentCtx)
	_, rootSpan := p.StartSpan(rootCtx, "task.execute")
	rootSpan.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1, "only the inner span has ended; outer parent stays open")
	assert.False(t, spans[0].Parent.IsValid(),
		"new-root span must not inherit OTel parent installed by the outer StartSpan")
}

// Sanity check: marking a brand-new ctx as new-root is a no-op
// (nothing to scrub) and produces the same root-trace shape as a
// plain StartSpan on context.Background. Adapters that silently honor
// the marker on already-rootless ctx are correct.
func TestStartSpan_NewRootSpan_PlainCtx_StillProducesRoot(t *testing.T) {
	p, exp := newRecordingProvider(t)
	ctx := core.WithNewRootSpan(context.Background())

	_, span := p.StartSpan(ctx, "root-from-empty")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.False(t, spans[0].Parent.IsValid(),
		"new-root on plain ctx must produce a root span")
}

// The new-root marker composes with StartSpanLinked: the result is a
// root span (no parent inherited) carrying the supplied links. This is
// the exact shape ext/tasks uses for task.execute.
func TestStartSpanLinked_NewRootSpan_EmitsRootWithLinks(t *testing.T) {
	p, exp := newRecordingProvider(t)
	parentTC := core.TraceContext{Traceparent: linkTP1}
	ctx := core.WithTraceContext(context.Background(), parentTC)
	ctx = core.WithNewRootSpan(ctx)

	links := []core.Link{core.LinkFromTraceContext(parentTC)}
	_, span := p.StartSpanLinked(ctx, "task.execute", links,
		core.Attribute{Key: "mcp.task.id", Value: "t-root"},
	)
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.False(t, spans[0].Parent.IsValid(),
		"task.execute must be a root, not a child of the create span")
	require.Len(t, spans[0].Links, 1, "the link to the create span MUST survive the root-strip")
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", spans[0].Links[0].SpanContext.TraceID().String(),
		"link still points at the originating trace identity")
}
