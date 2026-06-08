package otel

// SEP-414 P6 — `mcpotel.NewTracerProvider` helper (issue 674).
//
// Examples wiring OTel against the mcpkit adapter end up building a
// fairly stereotyped `*sdktrace.TracerProvider`: an exporter, a
// Resource with `service.name` set so the observability backend
// indexes spans under a recognizable label, and either WithBatcher
// (production) or WithSyncer (demo / test). This file collects that
// boilerplate behind a single `NewTracerProvider(exp, opts...)`
// constructor so the consumer doesn't have to import
// go.opentelemetry.io/otel/sdk/resource + .../semconv just to set a
// service name.
//
// The functional-options pattern leaves room for future additions
// (`WithDeploymentEnvironment`, `WithServiceVersion`, `WithResource`
// escape hatch) without disturbing the constructor signature. The
// option set today is intentionally small — extend when a real
// consumer asks.
//
// Existing mcpotel.NewProvider(otelTP) is unchanged; this file adds a
// sibling helper that callers compose with it:
//
//	otelTP := mcpotel.NewTracerProvider(exp, mcpotel.WithServiceName("my-server"))
//	srv := server.NewServer(info, server.WithTracerProvider(mcpotel.NewProvider(otelTP)))

import (
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// TracerProviderOption mutates the SDK TracerProvider construction
// performed by NewTracerProvider. Pass any combination of the
// With... options below; later options override earlier ones when
// they touch the same config field.
type TracerProviderOption func(*tracerProviderConfig)

type tracerProviderConfig struct {
	serviceName string
	syncer      bool // true = WithSyncer; false (default) = WithBatcher
}

// WithServiceName sets the OpenTelemetry `service.name` Resource
// attribute on the constructed TracerProvider. Observability
// backends (Grafana / Tempo / Jaeger / Honeycomb) index spans by
// this attribute as the primary axis — without it, traces appear
// under `unknown_service:<binary>`.
//
// An empty name is treated as "leave default" — the caller may
// pass through a config-file value without branching on whether
// the field is populated. The SDK's default Resource still applies
// in that case (which usually means `unknown_service:<binary>` in
// Grafana, but at least the call site doesn't crash).
func WithServiceName(name string) TracerProviderOption {
	return func(c *tracerProviderConfig) {
		if name != "" {
			c.serviceName = name
		}
	}
}

// WithSyncer switches the TracerProvider from the default batched
// span processor to a synchronous one: every span ships immediately
// on End(), no batch-flush window. Slightly slower per-span at the
// transport layer (gRPC for OTLP, line write for stdout), but the
// trade-off is the right one for teaching demos and tests where an
// operator who kills the process seconds after `tools/call` returns
// must see their spans in the backend, not wait for a batch flush
// that never happens.
//
// Real high-throughput servers should stay on the default
// (WithBatcher) and handle SIGTERM with an explicit ForceFlush +
// Shutdown sequence.
func WithSyncer() TracerProviderOption {
	return func(c *tracerProviderConfig) {
		c.syncer = true
	}
}

// NewTracerProvider builds an `*sdktrace.TracerProvider` with the
// supplied exporter and options. The returned TracerProvider is
// ready to hand to mcpotel.NewProvider(otelTP) for wiring into
// server.WithTracerProvider or client.WithTracerProvider.
//
// Panics if exporter is nil — silently constructing a TracerProvider
// that emits no spans loses signals without surfacing the
// misconfiguration. Same fail-fast convention as
// mcpotel.NewProvider(nil otelTP).
//
// The default span processor is batched (production-aligned). Pass
// WithSyncer to switch to sync exporting. The default Resource is
// the SDK default (no service.name); pass WithServiceName to set it.
//
// Future options (`WithDeploymentEnvironment`, `WithServiceVersion`,
// `WithResource` escape hatch) compose against the same internal
// config — they will land when a real consumer asks. Issue 663 (P6
// umbrella) tracks the SEP-414 surface work that consumes this
// helper.
func NewTracerProvider(exporter sdktrace.SpanExporter, opts ...TracerProviderOption) *sdktrace.TracerProvider {
	if exporter == nil {
		panic("ext/otel: NewTracerProvider called with nil SpanExporter")
	}

	cfg := tracerProviderConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	sdkOpts := make([]sdktrace.TracerProviderOption, 0, 3)
	if cfg.serviceName != "" {
		sdkOpts = append(sdkOpts, sdktrace.WithResource(resource.NewSchemaless(
			semconv.ServiceName(cfg.serviceName),
		)))
	}
	if cfg.syncer {
		sdkOpts = append(sdkOpts, sdktrace.WithSyncer(exporter))
	} else {
		sdkOpts = append(sdkOpts, sdktrace.WithBatcher(exporter))
	}

	return sdktrace.NewTracerProvider(sdkOpts...)
}
