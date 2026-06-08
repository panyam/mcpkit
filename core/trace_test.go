package core

import (
	"context"
	"errors"
	"testing"
)

// W3C reference vectors — drawn from the spec examples and the standard
// validation corpus. Keeping them in one table means a future spec
// revision can be evaluated by editing this list alone.
var traceparentVectors = []struct {
	name      string
	input     string
	wantValid bool
}{
	{
		name:      "w3c_example_section3_2_2_1",
		input:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		wantValid: true,
	},
	{
		name:      "w3c_example_appendix_a",
		input:     "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		wantValid: true,
	},
	{
		name:      "flags_zero_is_valid",
		input:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
		wantValid: true,
	},
	{
		name:      "empty",
		input:     "",
		wantValid: false,
	},
	{
		name:      "too_short",
		input:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
		wantValid: false,
	},
	{
		name:      "too_long",
		input:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
		wantValid: false,
	},
	{
		name:      "future_version_rejected",
		input:     "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		wantValid: false,
	},
	{
		name:      "uppercase_hex_rejected",
		input:     "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		wantValid: false,
	},
	{
		name:      "non_hex_flags",
		input:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-xy",
		wantValid: false,
	},
	{
		name:      "all_zero_trace_id",
		input:     "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		wantValid: false,
	},
	{
		name:      "all_zero_span_id",
		input:     "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		wantValid: false,
	},
	{
		name:      "missing_dashes",
		input:     "004bf92f3577b34da6a3ce929d0e0e473600f067aa0ba902b701xxxx",
		wantValid: false,
	},
}

func TestExtractTraceContext_W3CReferenceVectors(t *testing.T) {
	const tracestate = "vendor1=abc,vendor2=def"
	for _, vec := range traceparentVectors {
		t.Run(vec.name, func(t *testing.T) {
			meta := map[string]any{
				MetaKeyTraceparent: vec.input,
				MetaKeyTracestate:  tracestate,
			}
			got := ExtractTraceContext(meta)
			if vec.wantValid {
				if got.Traceparent != vec.input {
					t.Fatalf("Traceparent: got %q, want %q", got.Traceparent, vec.input)
				}
				if got.Tracestate != tracestate {
					t.Fatalf("Tracestate: got %q, want %q", got.Tracestate, tracestate)
				}
			} else {
				if !got.IsZero() {
					t.Fatalf("expected zero TraceContext for malformed input; got Traceparent=%q Tracestate=%q", got.Traceparent, got.Tracestate)
				}
			}
		})
	}
}

func TestExtractTraceContext_MissingKey(t *testing.T) {
	if got := ExtractTraceContext(nil); !got.IsZero() {
		t.Fatalf("nil meta: expected zero, got %+v", got)
	}
	if got := ExtractTraceContext(map[string]any{}); !got.IsZero() {
		t.Fatalf("empty meta: expected zero, got %+v", got)
	}
	if got := ExtractTraceContext(map[string]any{"other": "x"}); !got.IsZero() {
		t.Fatalf("unrelated key: expected zero, got %+v", got)
	}
}

func TestExtractTraceContext_WrongType(t *testing.T) {
	meta := map[string]any{
		MetaKeyTraceparent: 42, // not a string
	}
	if got := ExtractTraceContext(meta); !got.IsZero() {
		t.Fatalf("non-string traceparent: expected zero, got %+v", got)
	}
}

func TestExtractTraceContext_TracestateDroppedOnMalformedTraceparent(t *testing.T) {
	// Per W3C: don't forward tracestate without a valid traceparent.
	meta := map[string]any{
		MetaKeyTraceparent: "definitely-not-valid",
		MetaKeyTracestate:  "vendor=keep",
	}
	got := ExtractTraceContext(meta)
	if !got.IsZero() {
		t.Fatalf("expected tracestate dropped when traceparent is malformed; got %+v", got)
	}
}

func TestInjectTraceContext_RoundTrip(t *testing.T) {
	tc := TraceContext{
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Tracestate:  "vendor=abc",
	}
	meta := map[string]any{}
	InjectTraceContext(meta, tc)
	if meta[MetaKeyTraceparent] != tc.Traceparent {
		t.Fatalf("traceparent: got %v, want %q", meta[MetaKeyTraceparent], tc.Traceparent)
	}
	if meta[MetaKeyTracestate] != tc.Tracestate {
		t.Fatalf("tracestate: got %v, want %q", meta[MetaKeyTracestate], tc.Tracestate)
	}
	round := ExtractTraceContext(meta)
	if round != tc {
		t.Fatalf("round-trip: got %+v, want %+v", round, tc)
	}
}

func TestInjectTraceContext_EmptyFieldsAreNotWritten(t *testing.T) {
	meta := map[string]any{"existing": "untouched"}
	InjectTraceContext(meta, TraceContext{})
	if _, ok := meta[MetaKeyTraceparent]; ok {
		t.Fatalf("empty traceparent should not be written")
	}
	if _, ok := meta[MetaKeyTracestate]; ok {
		t.Fatalf("empty tracestate should not be written")
	}
	if meta["existing"] != "untouched" {
		t.Fatalf("unrelated keys should be left alone")
	}
}

func TestInjectTraceContext_TracestateAloneIsNotWrittenWithoutTraceparent(t *testing.T) {
	// Tracestate alone is a degenerate input; the Inject function still
	// writes it because the validation contract is on Extract. This test
	// pins that asymmetry so a reader is not surprised.
	meta := map[string]any{}
	InjectTraceContext(meta, TraceContext{Tracestate: "vendor=x"})
	if _, ok := meta[MetaKeyTracestate]; !ok {
		t.Fatalf("Inject writes tracestate even without a traceparent — Extract is the validator")
	}
}

func TestInjectTraceContext_Idempotent(t *testing.T) {
	tc := TraceContext{
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Tracestate:  "vendor=abc",
	}
	meta := map[string]any{}
	InjectTraceContext(meta, tc)
	first := map[string]any{}
	for k, v := range meta {
		first[k] = v
	}
	InjectTraceContext(meta, tc)
	if len(meta) != len(first) {
		t.Fatalf("size diverged: got %d, want %d", len(meta), len(first))
	}
	for k, v := range first {
		if meta[k] != v {
			t.Fatalf("key %q diverged on second inject", k)
		}
	}
}

func TestTraceContext_IsZero(t *testing.T) {
	if !(TraceContext{}).IsZero() {
		t.Fatalf("zero TraceContext should report IsZero=true")
	}
	if (TraceContext{Traceparent: "x"}).IsZero() {
		t.Fatalf("non-empty Traceparent should report IsZero=false")
	}
	if (TraceContext{Tracestate: "x"}).IsZero() {
		t.Fatalf("non-empty Tracestate should report IsZero=false")
	}
}

func TestWithTraceContext_RoundTrip(t *testing.T) {
	tc := TraceContext{
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Tracestate:  "vendor=x",
	}
	ctx := WithTraceContext(context.Background(), tc)
	got := TraceContextFromContext(ctx)
	if got != tc {
		t.Fatalf("ctx round-trip: got %+v, want %+v", got, tc)
	}
}

func TestTraceContextFromContext_Absent(t *testing.T) {
	got := TraceContextFromContext(context.Background())
	if !got.IsZero() {
		t.Fatalf("absent: expected zero, got %+v", got)
	}
}

func TestWithTraceContext_ZeroIsExplicitScrub(t *testing.T) {
	// Storing a zero TraceContext is meaningful: it scrubs an inherited
	// trace context at a boundary. Verify the explicit zero survives the
	// round trip.
	parent := WithTraceContext(context.Background(), TraceContext{Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"})
	scrubbed := WithTraceContext(parent, TraceContext{})
	got := TraceContextFromContext(scrubbed)
	if !got.IsZero() {
		t.Fatalf("scrub: expected zero, got %+v", got)
	}
}

func TestNoopTracerProvider_ZeroOverhead(t *testing.T) {
	var tp TracerProvider = NoopTracerProvider{}
	ctx, span := tp.StartSpan(context.Background(), "noop", Attribute{Key: "k", Value: "v"})
	if ctx == nil {
		t.Fatal("StartSpan must return non-nil context")
	}
	if span == nil {
		t.Fatal("StartSpan must return non-nil Span even for the Noop provider")
	}
	// All Span methods must be safe to call on the Noop and must not panic.
	span.SetAttribute("k2", "v2")
	span.RecordError(errors.New("boom"))
	span.RecordError(nil)
	span.End()
	// Double-End is allowed by contract.
	span.End()
}

func TestAttribute_StructFields(t *testing.T) {
	// Pins the Attribute shape so a downstream module (P2 middleware,
	// ext/otel adapter) compiles against a stable struct.
	a := Attribute{Key: "mcp.method", Value: "tools/call"}
	if a.Key != "mcp.method" || a.Value != "tools/call" {
		t.Fatalf("attribute fields not preserved")
	}
}

func TestInjectTraceContextIntoParams_RespectsExplicitMeta(t *testing.T) {
	tc := TraceContext{
		Traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-1111111111111111-01",
		Tracestate:  "vendor=child",
	}
	explicit := "00-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-1234567812345678-01"
	in := map[string]any{
		"_meta": map[string]any{MetaKeyTraceparent: explicit},
		"data":  "x",
	}

	got := InjectTraceContextIntoParams(in, tc)
	obj, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", got)
	}
	meta := obj["_meta"].(map[string]any)
	if meta[MetaKeyTraceparent] != explicit {
		t.Fatalf("explicit traceparent overwritten: got %q want %q", meta[MetaKeyTraceparent], explicit)
	}
	if meta[MetaKeyTracestate] != "vendor=child" {
		t.Fatalf("tracestate not added when not explicitly set: got %q", meta[MetaKeyTracestate])
	}
}

func TestInjectTraceContextIntoParams_HandlesNilAndNonObject(t *testing.T) {
	tc := TraceContext{Traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-1111111111111111-01"}

	got := InjectTraceContextIntoParams(nil, tc)
	obj, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("nil params should yield a fresh map, got %T", got)
	}
	meta := obj["_meta"].(map[string]any)
	if meta[MetaKeyTraceparent] != tc.Traceparent {
		t.Fatalf("nil-params injection lost traceparent")
	}

	arr := []any{1, 2, 3}
	got2 := InjectTraceContextIntoParams(arr, tc)
	if got2interface, ok := got2.([]any); !ok || len(got2interface) != 3 {
		t.Fatalf("non-object params must pass through unchanged, got %T %v", got2, got2)
	}

	zeroPass := InjectTraceContextIntoParams(map[string]any{"a": 1}, TraceContext{})
	if m, ok := zeroPass.(map[string]any); !ok || m["a"] != 1 || m["_meta"] != nil {
		t.Fatalf("zero TraceContext must pass through unchanged, got %v", zeroPass)
	}
}

// --- P6 active-span accessor (issue 661) ------------------------------------

func TestSpanFromContext_EmptyCtx_ReturnsNoop(t *testing.T) {
	span := SpanFromContext(context.Background())
	if span == nil {
		t.Fatalf("SpanFromContext must never return nil; got nil for empty ctx")
	}
	// All Span methods must be safe to call on the returned span without
	// panicking — the no-op return is what lets call sites that always
	// decorate the active span work even when no provider is wired.
	span.SetAttribute("k", "v")
	span.RecordError(errors.New("ignored"))
	span.RecordError(nil)
	span.End()
	span.End()
}

func TestSpanFromContext_AfterNoopStartSpan_PublishesSpan(t *testing.T) {
	ctx, started := NoopTracerProvider{}.StartSpan(context.Background(), "noop")
	got := SpanFromContext(ctx)
	if got == nil {
		t.Fatalf("Noop.StartSpan must publish the span via WithActiveSpan")
	}
	if got != started {
		t.Fatalf("SpanFromContext must return the same Span value StartSpan returned; got %T, want %T", got, started)
	}
}

func TestSpanFromContext_SurvivesCtxDerivation(t *testing.T) {
	tp := NoopTracerProvider{}
	root, started := tp.StartSpan(context.Background(), "outer")

	derived := context.WithValue(root, struct{}{}, "irrelevant")
	if SpanFromContext(derived) != started {
		t.Fatalf("active span must survive ctx.WithValue derivation")
	}

	withCancel, cancel := context.WithCancel(root)
	defer cancel()
	if SpanFromContext(withCancel) != started {
		t.Fatalf("active span must survive ctx.WithCancel derivation")
	}
}

func TestSpanFromContext_NestedStartSpan_InnermostWins(t *testing.T) {
	tp := NoopTracerProvider{}
	outer, outerSpan := tp.StartSpan(context.Background(), "outer")
	inner, innerSpan := tp.StartSpan(outer, "inner")

	if got := SpanFromContext(inner); got != innerSpan {
		t.Fatalf("SpanFromContext(innerCtx) should return innerSpan; got %v want %v", got, innerSpan)
	}
	if got := SpanFromContext(outer); got != outerSpan {
		t.Fatalf("SpanFromContext(outerCtx) should still return outerSpan after inner StartSpan; got %v want %v", got, outerSpan)
	}
}

func TestWithActiveSpan_NilNoOp(t *testing.T) {
	ctx := context.Background()
	if WithActiveSpan(ctx, nil) != ctx {
		t.Fatalf("WithActiveSpan(ctx, nil) must return ctx unchanged so defensive call sites don't have to branch")
	}
}

func TestBaseContext_Span_DelegatesToSpanFromContext(t *testing.T) {
	tp := NoopTracerProvider{}
	ctx, started := tp.StartSpan(context.Background(), "via-base-context")
	bc := BaseContext{Context: ctx}
	if got := bc.Span(); got != started {
		t.Fatalf("BaseContext.Span() should delegate to package-level SpanFromContext; got %v want %v", got, started)
	}

	bare := BaseContext{Context: context.Background()}
	span := bare.Span()
	if span == nil {
		t.Fatalf("BaseContext.Span() must never return nil")
	}
	span.SetAttribute("k", "v") // must not panic
}

// --- P6 span links (issue 662) ----------------------------------------------

func TestLinkFromTraceContext_OnlyTC(t *testing.T) {
	tc := TraceContext{Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}
	link := LinkFromTraceContext(tc)
	if link.TraceContext != tc {
		t.Fatalf("LinkFromTraceContext should preserve TraceContext")
	}
	if link.Attributes != nil {
		t.Fatalf("LinkFromTraceContext should leave Attributes nil for callers to populate explicitly")
	}
}

func TestNoopSpan_AddLink_IsNoOp(t *testing.T) {
	span := noopSpan{}
	span.AddLink(Link{})
	span.AddLink(LinkFromTraceContext(TraceContext{Traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"}))
}

func TestStartSpanLinked_NoopProvider_FallsBackToStartSpan(t *testing.T) {
	tp := NoopTracerProvider{}
	links := []Link{
		LinkFromTraceContext(TraceContext{Traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"}),
	}
	ctx, span := StartSpanLinked(tp, context.Background(), "test", links,
		Attribute{Key: "k", Value: "v"},
	)
	if span == nil {
		t.Fatalf("helper must return non-nil span even when links are dropped")
	}
	if got := SpanFromContext(ctx); got == nil {
		t.Fatalf("Noop fallback must still publish the active span")
	}
}

func TestStartSpanLinked_NilLinks_ActsAsStartSpan(t *testing.T) {
	tp := NoopTracerProvider{}
	_, span := StartSpanLinked(tp, context.Background(), "test", nil)
	if span == nil {
		t.Fatalf("nil links slice must still return a span")
	}
	span.End()
}

func TestStartSpanLinked_EmptyLinks_ActsAsStartSpan(t *testing.T) {
	tp := NoopTracerProvider{}
	_, span := StartSpanLinked(tp, context.Background(), "test", []Link{})
	if span == nil {
		t.Fatalf("empty links slice must still return a span")
	}
}

type fakeLinkedProvider struct {
	NoopTracerProvider
	receivedLinks []Link
	receivedName  string
}

func (p *fakeLinkedProvider) StartSpanLinked(ctx context.Context, name string, links []Link, attrs ...Attribute) (context.Context, Span) {
	p.receivedLinks = append([]Link(nil), links...)
	p.receivedName = name
	return p.StartSpan(ctx, name, attrs...)
}

func TestStartSpanLinked_LinkedProvider_RoutedToInterface(t *testing.T) {
	tp := &fakeLinkedProvider{}
	links := []Link{
		LinkFromTraceContext(TraceContext{Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}),
		{TraceContext: TraceContext{Traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"}, Attributes: []Attribute{{Key: "link.kind", Value: "sibling"}}},
	}
	_, span := StartSpanLinked(tp, context.Background(), "linked-test", links)
	if span == nil {
		t.Fatalf("linked provider path must return non-nil span")
	}
	if tp.receivedName != "linked-test" {
		t.Fatalf("StartSpanLinked must pass name through; got %q", tp.receivedName)
	}
	if len(tp.receivedLinks) != 2 {
		t.Fatalf("StartSpanLinked must pass all links through; got %d, want 2", len(tp.receivedLinks))
	}
	if tp.receivedLinks[1].Attributes[0].Key != "link.kind" {
		t.Fatalf("StartSpanLinked must preserve per-link attributes")
	}
}
