// Package otel — issue 7 MeterProvider adapter.
//
// MeterProvider here wraps a go.opentelemetry.io/otel/metric.MeterProvider
// (typically constructed via go.opentelemetry.io/otel/sdk/metric.NewMeterProvider)
// and exposes it through the dependency-free core.MeterProvider
// contract mcpkit's dispatch path expects. One MeterProvider per
// server is the common case; the underlying OTel MeterProvider is
// the unit of exporter configuration, so multiple seam-side wrappers
// pointing at the same OTel MeterProvider share one OTLP / Prometheus
// pipeline.
//
// Trace exemplars are wired automatically: every Add / Record call
// forwards the ctx unchanged, and the OTel SDK's default exemplar
// filter (AlwaysOnSampleParent) extracts the active span context to
// stamp on the measurement. Backends that surface exemplars
// (Grafana + Mimir + Tempo via the LGTM stack) render the
// metric-to-trace pivot as clickable dots on histogram panels —
// closes the observability loop the SEP-414 work opened.

package otel

import (
	"context"

	core "github.com/panyam/mcpkit/core"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// defaultMeterInstrumentationName is the value passed to MeterProvider.Meter
// when WithMeterInstrumentationName is not supplied. Identifies
// mcpkit-emitted metric instruments in OTel-aware backends — Mimir's
// `__name__` filter, Prometheus' `instrumentation.scope.name` label.
const defaultMeterInstrumentationName = "github.com/panyam/mcpkit/server"

// MeterProvider wraps an OpenTelemetry MeterProvider and exposes it
// through the dependency-free core.MeterProvider contract. Construct
// via NewMeterProvider; pair with the OTel SDK side (sdk/metric).
//
// MeterProvider is safe for concurrent use. The internal Meter is
// created once at construction so subsequent Int64Counter /
// Float64Histogram / Int64UpDownCounter factory calls never re-look
// up the Meter on the hot path.
type MeterProvider struct {
	meter otelmetric.Meter

	// otelMP is kept so OTelMeterProvider() can hand the underlying
	// TP back to downstream libraries that take an OTel MeterProvider
	// directly. Multiple consumers sharing one MeterProvider keeps
	// the whole process on a single OTLP / Prometheus pipeline.
	otelMP otelmetric.MeterProvider
}

// MeterOption mutates a meterProviderConfig during NewMeterProvider.
// Exported so user-side libraries can layer their own helpers without
// depending on the unexported config shape — matches the trace-side
// Option pattern.
type MeterOption func(*meterProviderConfig)

type meterProviderConfig struct {
	instrumentationName string
}

// WithMeterInstrumentationName overrides the OTel instrumentation
// library name used when constructing the Meter from the OTel
// MeterProvider. Backends index metrics by this name; leave the
// default unless your server embeds mcpkit inside a larger SDK and
// you want a more specific identifier.
//
// Empty name reverts to the package default.
func WithMeterInstrumentationName(name string) MeterOption {
	return func(cfg *meterProviderConfig) {
		if name != "" {
			cfg.instrumentationName = name
		}
	}
}

// NewMeterProvider constructs a MeterProvider backed by the given
// OTel MeterProvider. Panics if otelMP is nil — a wrapper without a
// backing provider would silently lose measurements, so the check
// fails fast at wiring time. Matches the NewProvider trace-side
// contract.
//
// Typical wiring:
//
//	import (
//	    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
//	    "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
//	    mcpotel "github.com/panyam/mcpkit/ext/otel"
//	)
//
//	exp, _ := otlpmetricgrpc.New(ctx)
//	otelMP := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
//	defer otelMP.Shutdown(ctx)
//
//	srv := server.NewServer(info, server.WithMeterProvider(mcpotel.NewMeterProvider(otelMP)))
func NewMeterProvider(otelMP otelmetric.MeterProvider, opts ...MeterOption) *MeterProvider {
	if otelMP == nil {
		panic("ext/otel: NewMeterProvider called with nil MeterProvider")
	}
	cfg := meterProviderConfig{instrumentationName: defaultMeterInstrumentationName}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &MeterProvider{
		meter:  otelMP.Meter(cfg.instrumentationName),
		otelMP: otelMP,
	}
}

// OTelMeterProvider returns the underlying OpenTelemetry MeterProvider
// the wrapper was constructed with. Used by callers that need to hand
// the same MeterProvider to downstream libraries which take an OTel
// MeterProvider directly — so the whole process shares one metrics
// pipeline.
//
// The returned value is the same pointer NewMeterProvider was called
// with; nil-checks on the caller side are unnecessary
// (NewMeterProvider panics on nil).
func (p *MeterProvider) OTelMeterProvider() otelmetric.MeterProvider {
	return p.otelMP
}

// Int64Counter implements core.MeterProvider. Each call constructs a
// fresh OTel instrument; instrument-creation errors fall back to a
// no-op counter so a misconfigured Meter never crashes the dispatch
// install — matches the trace-side defensive contract on Provider.
// The fallback path logs no warning because OTel instrument creation
// only fails on duplicate-with-conflicting-config errors that the
// SDK already loudly surfaces via its own logger.
func (p *MeterProvider) Int64Counter(name string, opts ...core.InstrumentOption) core.Int64Counter {
	cfg := core.ApplyInstrumentOptions(opts...)
	c, err := p.meter.Int64Counter(name, toOTelInstrumentOptions(cfg)...)
	if err != nil {
		return core.NoopMeterProvider{}.Int64Counter(name)
	}
	return &otelInt64Counter{counter: c}
}

// Float64Histogram implements core.MeterProvider. See Int64Counter
// for the construction-failure handling.
func (p *MeterProvider) Float64Histogram(name string, opts ...core.InstrumentOption) core.Float64Histogram {
	cfg := core.ApplyInstrumentOptions(opts...)
	h, err := p.meter.Float64Histogram(name, toOTelHistogramOptions(cfg)...)
	if err != nil {
		return core.NoopMeterProvider{}.Float64Histogram(name)
	}
	return &otelFloat64Histogram{histogram: h}
}

// Int64UpDownCounter implements core.MeterProvider. See Int64Counter
// for the construction-failure handling.
func (p *MeterProvider) Int64UpDownCounter(name string, opts ...core.InstrumentOption) core.Int64UpDownCounter {
	cfg := core.ApplyInstrumentOptions(opts...)
	c, err := p.meter.Int64UpDownCounter(name, toOTelUpDownCounterOptions(cfg)...)
	if err != nil {
		return core.NoopMeterProvider{}.Int64UpDownCounter(name)
	}
	return &otelInt64UpDownCounter{counter: c}
}

// --- instrument wrappers -----------------------------------------------------

type otelInt64Counter struct {
	counter otelmetric.Int64Counter
}

// Add forwards to the underlying OTel counter with the ctx
// unchanged. The OTel SDK's default exemplar filter
// (AlwaysOnSampleParent) reads the active span context from ctx to
// stamp an exemplar on the measurement — no manual exemplar wiring
// here. Attributes are converted to OTel attribute.KeyValue.
func (c *otelInt64Counter) Add(ctx context.Context, value int64, attrs ...core.Attribute) {
	c.counter.Add(ctx, value, otelmetric.WithAttributes(toOTelAttributes(attrs)...))
}

type otelFloat64Histogram struct {
	histogram otelmetric.Float64Histogram
}

// Record forwards to the underlying OTel histogram with ctx
// unchanged. See otelInt64Counter.Add for the exemplar contract.
func (h *otelFloat64Histogram) Record(ctx context.Context, value float64, attrs ...core.Attribute) {
	h.histogram.Record(ctx, value, otelmetric.WithAttributes(toOTelAttributes(attrs)...))
}

type otelInt64UpDownCounter struct {
	counter otelmetric.Int64UpDownCounter
}

// Add forwards to the underlying OTel up-down counter. Negative
// values are permitted — that's the up-down contract.
func (c *otelInt64UpDownCounter) Add(ctx context.Context, value int64, attrs ...core.Attribute) {
	c.counter.Add(ctx, value, otelmetric.WithAttributes(toOTelAttributes(attrs)...))
}

// --- option / attribute conversion ------------------------------------------

func toOTelInstrumentOptions(cfg core.InstrumentConfig) []otelmetric.Int64CounterOption {
	out := make([]otelmetric.Int64CounterOption, 0, 2)
	if cfg.Description != "" {
		out = append(out, otelmetric.WithDescription(cfg.Description))
	}
	if cfg.Unit != "" {
		out = append(out, otelmetric.WithUnit(cfg.Unit))
	}
	return out
}

func toOTelHistogramOptions(cfg core.InstrumentConfig) []otelmetric.Float64HistogramOption {
	out := make([]otelmetric.Float64HistogramOption, 0, 2)
	if cfg.Description != "" {
		out = append(out, otelmetric.WithDescription(cfg.Description))
	}
	if cfg.Unit != "" {
		out = append(out, otelmetric.WithUnit(cfg.Unit))
	}
	return out
}

func toOTelUpDownCounterOptions(cfg core.InstrumentConfig) []otelmetric.Int64UpDownCounterOption {
	out := make([]otelmetric.Int64UpDownCounterOption, 0, 2)
	if cfg.Description != "" {
		out = append(out, otelmetric.WithDescription(cfg.Description))
	}
	if cfg.Unit != "" {
		out = append(out, otelmetric.WithUnit(cfg.Unit))
	}
	return out
}

func toOTelAttributes(attrs []core.Attribute) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, attribute.String(a.Key, a.Value))
	}
	return out
}
