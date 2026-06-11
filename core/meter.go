package core

import "context"

// Issue 7 — MeterProvider seam.
//
// This file defines the dependency-free metrics contract that
// mcpkit's dispatch path emits through. It mirrors the
// SEP-414 TracerProvider shape: a minimal interface in core, an
// always-installed NoopMeterProvider, and a separate adapter in
// ext/otel that wraps the real OpenTelemetry SDK. The base module
// stays dep-free; adopters opt into a real meter via
// server.WithMeterProvider.
//
// Why a separate seam (and not just "use ext/otel directly"):
//   - Keeps the base module free of go.opentelemetry.io/otel/metric.
//   - Lets test code and alternate backends (Prometheus, expvar)
//     implement a 3-method interface instead of OTel's full
//     instrument hierarchy.
//   - Matches the precedent the TracerProvider seam set (issue 312
//     P1 / PR 644).
//
// What this file does NOT do:
//   - Emit any metrics (the dispatch-side instrumentation lives in
//     server/metrics_middleware.go).
//   - Provide an OTel adapter (ext/otel/meter.go).
//
// Logs and traces also need observability, but the seam shapes
// differ:
//   - Traces emit a wire SEP (SEP-414) because span context crosses
//     the network. Logs and metrics are emit-locally signals — no
//     wire contract is needed.
//   - Stdlib slog already IS the dep-free logs seam, so no
//     core.LoggerProvider exists.
//
// Issue 7's modernization narrative + the per-signal asymmetry are
// documented in issue 7 and docs/SEP_414_OTEL.md.

// MeterProvider is the minimal metrics seam mcpkit components
// consume. Implementations construct instruments (counters,
// histograms, up-down counters) at install time and hand them to
// the dispatch middleware that records measurements per request.
//
// Implementations MUST:
//   - Be safe for concurrent use by multiple goroutines.
//   - Return a non-nil instrument from every factory method even
//     when the provider is a no-op — call sites do not nil-check
//     returned instruments.
//   - Treat instrument construction as the only allocation-heavy
//     step; subsequent measurements (Add / Record) should be hot
//     path.
//
// The default implementation, NoopMeterProvider, returns shared
// no-op instrument singletons and performs no allocation on
// measurement.
type MeterProvider interface {
	// Int64Counter returns a monotonic Int64Counter instrument named
	// `name`. Call sites typically use this for "events seen" style
	// counts (tools/call dispatches, JSON-RPC errors). Re-calling
	// with the same name MAY return the same underlying instrument
	// (adapter-dependent) — call sites store the returned instrument
	// once at install time and reuse it.
	Int64Counter(name string, opts ...InstrumentOption) Int64Counter

	// Float64Histogram returns a Float64Histogram instrument named
	// `name`. Use for latency / duration / size distributions where
	// percentiles matter. Adapters choose bucket boundaries — the
	// seam does not surface them so swappable backends (OTel's
	// exponential default, Prometheus' fixed-bucket variant) are
	// drop-in compatible.
	Float64Histogram(name string, opts ...InstrumentOption) Float64Histogram

	// Int64UpDownCounter returns an Int64UpDownCounter instrument
	// named `name`. Use for "currently active" gauges where the
	// caller knows the deltas (active sessions, queued requests).
	// Distinct from OTel's "observable gauge" — the seam keeps the
	// synchronous-only flavor because mcpkit's dispatch path always
	// knows the delta at the call site.
	Int64UpDownCounter(name string, opts ...InstrumentOption) Int64UpDownCounter
}

// Int64Counter is a monotonic counter measured in int64 units.
// Add receives the active context so adapters that support
// exemplars (OTel SDK) can read the active span via
// SpanFromContext and attach it to the measurement. Adapters
// without exemplar support ignore ctx.
//
// Negative values are forbidden by OTel spec; mcpkit-side
// instrumentation should never pass a negative value. Adapters
// MAY clamp or panic per their underlying SDK's contract — the
// seam does not enforce.
type Int64Counter interface {
	// Add records a non-negative delta against the counter. Attributes
	// scope the measurement along the named dimensions (e.g.
	// `tool=fetch`, `code=-32601`).
	Add(ctx context.Context, value int64, attrs ...Attribute)
}

// Float64Histogram is a distribution-recording instrument measured
// in float64 units. Record receives the active context so adapters
// can extract exemplars from the surrounding span.
type Float64Histogram interface {
	// Record records a single observation. Negative values are
	// permitted by the OTel histogram contract (e.g. time-deltas
	// expressed as negative seconds) — adapters that disallow them
	// surface the rejection via their own contract.
	Record(ctx context.Context, value float64, attrs ...Attribute)
}

// Int64UpDownCounter is a bidirectional counter measured in int64
// units. Use for "currently N" gauges where the caller knows the
// delta (e.g. session create → +1, session expire → -1). Combine
// with attributes if the gauge spans dimensions (e.g.
// `state=initializing`).
type Int64UpDownCounter interface {
	// Add records a signed delta. Negative values are permitted and
	// expected — that's the up-down contract.
	Add(ctx context.Context, value int64, attrs ...Attribute)
}

// InstrumentConfig is the resolved configuration for a single
// instrument: description (free-form text for observability backends
// to render in tooltips / docs) and unit (UCUM-style string like
// `"ms"`, `"By"`, `"1"`). Adapters convert these to their backend's
// native concept (OTel uses identical fields; Prometheus encodes
// unit into the metric name suffix).
//
// Fields are exported so adapters in different go.mod can read them
// without a getter dance. Both fields are optional — a zero
// InstrumentConfig is a valid instrument with no description and
// no unit.
type InstrumentConfig struct {
	// Description is free-form documentation surfaced by the backend.
	// May include semantic-conventions language (e.g. "Number of
	// tools/call requests dispatched."). Empty string is treated as
	// "no description" by every adapter.
	Description string

	// Unit follows the UCUM convention (https://ucum.org/) — e.g.
	// `"ms"` for milliseconds, `"By"` for bytes, `"1"` for
	// dimensionless. Empty string is treated as "no unit". Adapters
	// pass this through verbatim; backends that need a different
	// dialect (Prometheus' suffix style) translate internally.
	Unit string
}

// InstrumentOption mutates an InstrumentConfig during instrument
// construction. The Option type is exported so adapters and
// instrument call sites can layer their own helpers without
// depending on the unexported config shape — matches the
// ext/otel.Option pattern.
type InstrumentOption func(*InstrumentConfig)

// WithDescription sets the instrument description. Last call wins
// on duplicate; empty string is a no-op (preserves any earlier
// setting).
func WithDescription(d string) InstrumentOption {
	return func(c *InstrumentConfig) {
		if d != "" {
			c.Description = d
		}
	}
}

// WithUnit sets the instrument unit. Last call wins on duplicate;
// empty string is a no-op.
func WithUnit(u string) InstrumentOption {
	return func(c *InstrumentConfig) {
		if u != "" {
			c.Unit = u
		}
	}
}

// ApplyInstrumentOptions runs opts against a fresh InstrumentConfig
// and returns the resolved value. Adapters call this once at the
// top of each factory method to translate the variadic option list
// into the backend's instrument construction arguments.
//
// Provided as a helper so adapters do not duplicate the
// "var cfg; for _, o := range opts { o(&cfg) }; return cfg" boilerplate
// in three factory methods.
func ApplyInstrumentOptions(opts ...InstrumentOption) InstrumentConfig {
	var cfg InstrumentConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// NoopMeterProvider is the default MeterProvider used when no
// metrics are configured. Every factory method returns a shared
// no-op singleton; every measurement call returns immediately
// without allocating. Safe to embed in tests, production wiring
// where metrics are disabled, and zero-overhead serve loops.
type NoopMeterProvider struct{}

// Int64Counter returns the shared no-op counter singleton.
func (NoopMeterProvider) Int64Counter(string, ...InstrumentOption) Int64Counter {
	return noopInt64Counter{}
}

// Float64Histogram returns the shared no-op histogram singleton.
func (NoopMeterProvider) Float64Histogram(string, ...InstrumentOption) Float64Histogram {
	return noopFloat64Histogram{}
}

// Int64UpDownCounter returns the shared no-op up-down counter
// singleton.
func (NoopMeterProvider) Int64UpDownCounter(string, ...InstrumentOption) Int64UpDownCounter {
	return noopInt64UpDownCounter{}
}

type noopInt64Counter struct{}

func (noopInt64Counter) Add(context.Context, int64, ...Attribute) {}

type noopFloat64Histogram struct{}

func (noopFloat64Histogram) Record(context.Context, float64, ...Attribute) {}

type noopInt64UpDownCounter struct{}

func (noopInt64UpDownCounter) Add(context.Context, int64, ...Attribute) {}
