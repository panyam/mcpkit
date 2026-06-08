// Package otel adapts the OpenTelemetry Go SDK (go.opentelemetry.io/otel)
// to mcpkit's dependency-free core.TracerProvider contract. Wire a Provider
// constructed from a real otelsdk.TracerProvider into the server via
// server.WithTracerProvider, and the SEP-414 P2 trace middleware will emit
// inbound spans on every JSON-RPC dispatch plus propagate W3C trace context
// on every outbound notification / server-to-client request.
//
// Phase 4 deliverable for SEP-414 (issue 312). Phase 2 (PR 649) shipped the
// dispatch-path wiring; this package is the SDK-backed adapter that turns
// those spans into something an exporter (stdout, OTLP, Jaeger, ...) can
// publish.
//
// Phase 3 (client-side spans) and Phase 5 (the polished examples/otel/
// walkthrough doc) are tracked separately on issue 312.
package otel

import (
	"context"

	core "github.com/panyam/mcpkit/core"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// defaultInstrumentationName is the value passed to TracerProvider.Tracer
// when WithInstrumentationName is not supplied. Identifies mcpkit-emitted
// spans in OTel-aware observability stacks (Jaeger UI's "Service" filter,
// for example).
const defaultInstrumentationName = "github.com/panyam/mcpkit/server"

// Provider wraps an OpenTelemetry TracerProvider and exposes it through the
// dependency-free core.TracerProvider contract that mcpkit's dispatch path
// expects. One Provider per server is the common case; the underlying OTel
// TracerProvider is the unit of exporter configuration and span batching,
// so multiple Providers backed by the same OTel TracerProvider share the
// same exporter pipeline.
//
// Provider is safe for concurrent use. The internal Tracer is created
// once at construction time so StartSpan never pays the Tracer-lookup
// cost on the hot path.
type Provider struct {
	tracer oteltrace.Tracer
}

// Option mutates a providerConfig during NewProvider. The Option type is
// exported so user-side libraries can layer their own helpers (e.g.,
// reading instrumentation name from a config struct) without depending on
// the unexported config shape.
type Option func(*providerConfig)

type providerConfig struct {
	instrumentationName string
}

// WithInstrumentationName overrides the OTel instrumentation library name
// used when constructing the Tracer from the OTel TracerProvider. The
// instrumentation name is what observability backends use to group spans
// by emitting library — leave the default unless your server embeds
// mcpkit inside a larger SDK and you want a more specific identifier.
//
// Empty name reverts to the package default.
func WithInstrumentationName(name string) Option {
	return func(cfg *providerConfig) {
		if name != "" {
			cfg.instrumentationName = name
		}
	}
}

// NewProvider constructs a Provider backed by the given OTel TracerProvider.
// Panics if otelTP is nil — a Provider without a real backing TracerProvider
// would silently lose spans, so the check fails fast at wiring time.
//
// Typical wiring:
//
//	import (
//	    sdktrace "go.opentelemetry.io/otel/sdk/trace"
//	    "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
//	    mcpotel "github.com/panyam/mcpkit/ext/otel"
//	)
//
//	exp, _ := stdouttrace.New()
//	otelTP := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
//	defer otelTP.Shutdown(ctx)
//
//	srv := server.NewServer(info, server.WithTracerProvider(mcpotel.NewProvider(otelTP)))
func NewProvider(otelTP oteltrace.TracerProvider, opts ...Option) *Provider {
	if otelTP == nil {
		panic("ext/otel: NewProvider called with nil TracerProvider")
	}
	cfg := providerConfig{instrumentationName: defaultInstrumentationName}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Provider{tracer: otelTP.Tracer(cfg.instrumentationName)}
}

// StartSpan implements core.TracerProvider. The returned context carries
// BOTH the OTel span context (so any OTel-instrumented library called by
// the handler sees the new span as parent) AND a mcpkit core.TraceContext
// updated to reflect the new span (so the SEP-414 P2 outbound _meta
// injection wraps stamp the wire with the child span's traceparent
// rather than the inbound parent's).
//
// Inbound trace context resolution:
//   - If ctx already carries a non-zero core.TraceContext (attached by
//     the server's traceMiddleware after _meta extraction or the SEP-2028
//     HTTP header bridge), it is parsed into an OTel SpanContext and
//     installed as the parent via trace.ContextWithSpanContext.
//   - Otherwise the OTel SDK's default propagation behavior applies (the
//     span starts a fresh trace).
//
// Attribute mapping: each core.Attribute becomes an attribute.String key.
// P1's contract scopes attributes to string/string; typed attributes
// arrive when the core surface widens.
func (p *Provider) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	return p.startSpanInternal(ctx, name, nil, attrs)
}

// StartSpanLinked implements core.LinkedTracerProvider — the option-at-start
// variant of StartSpan that attaches one or more causal Links to the new
// span (in addition to any parent extracted from ctx). Each Link entry
// whose TraceContext is zero or fails W3C validation is silently dropped
// (caller does not need to pre-filter), so callers can build the slice
// from raw inputs of varying validity. Per-link Attributes are mapped
// through as attribute.String entries on the resulting OTel link.
//
// Implementation: routes the same parent-install + span-start +
// active-span publish dance as StartSpan, with a WithLinks option
// appended to the startOpts slice. Empty / nil links behaves identically
// to StartSpan.
func (p *Provider) StartSpanLinked(ctx context.Context, name string, links []core.Link, attrs ...core.Attribute) (context.Context, core.Span) {
	return p.startSpanInternal(ctx, name, links, attrs)
}

// startSpanInternal is the shared body of StartSpan and StartSpanLinked.
// The split exists so adding a third variant (events bus seam etc.)
// doesn't duplicate the parent-install / WithTraceContext / WithActiveSpan
// sequence — only the option-list construction varies.
func (p *Provider) startSpanInternal(ctx context.Context, name string, links []core.Link, attrs []core.Attribute) (context.Context, core.Span) {
	if parent := core.TraceContextFromContext(ctx); !parent.IsZero() {
		if parentSC, ok := traceContextToSpanContext(parent); ok {
			ctx = oteltrace.ContextWithSpanContext(ctx, parentSC)
		}
	}

	startOpts := []oteltrace.SpanStartOption{}
	if len(attrs) > 0 {
		kvs := make([]attribute.KeyValue, 0, len(attrs))
		for _, a := range attrs {
			kvs = append(kvs, attribute.String(a.Key, a.Value))
		}
		startOpts = append(startOpts, oteltrace.WithAttributes(kvs...))
	}
	if len(links) > 0 {
		otelLinks := make([]oteltrace.Link, 0, len(links))
		for _, l := range links {
			if otelLink, ok := linkToOTelLink(l); ok {
				otelLinks = append(otelLinks, otelLink)
			}
		}
		if len(otelLinks) > 0 {
			startOpts = append(startOpts, oteltrace.WithLinks(otelLinks...))
		}
	}

	ctx, otelSpan := p.tracer.Start(ctx, name, startOpts...)

	if childTC := spanContextToTraceContext(otelSpan.SpanContext()); !childTC.IsZero() {
		ctx = core.WithTraceContext(ctx, childTC)
	}

	span := &Span{otel: otelSpan}
	// Publish the mcpkit Span wrapper via core.WithActiveSpan so inner
	// middleware and handler code can read it back via
	// core.SpanFromContext (or ctx.Span() on a BaseContext) and enrich
	// the active span with attributes — closes the P1 contract gap
	// called out by SEP-414 P6 (issue 661).
	ctx = core.WithActiveSpan(ctx, span)

	return ctx, span
}

// linkToOTelLink converts a core.Link into an OTel trace.Link by
// parsing the W3C TraceContext into an OTel SpanContext (Remote=true,
// matching the inbound-parent treatment in StartSpan) and mapping the
// per-link Attributes through as attribute.String entries. Returns
// (zero, false) when the TraceContext fails the same validation rules
// core.ExtractTraceContext applies — caller drops such links silently
// so downstream surfaces (tasks, events bus) can build link slices
// from raw inputs.
func linkToOTelLink(link core.Link) (oteltrace.Link, bool) {
	sc, ok := traceContextToSpanContext(link.TraceContext)
	if !ok {
		return oteltrace.Link{}, false
	}
	out := oteltrace.Link{SpanContext: sc}
	if len(link.Attributes) > 0 {
		kvs := make([]attribute.KeyValue, 0, len(link.Attributes))
		for _, a := range link.Attributes {
			kvs = append(kvs, attribute.String(a.Key, a.Value))
		}
		out.Attributes = kvs
	}
	return out, true
}
