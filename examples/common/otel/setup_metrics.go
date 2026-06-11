package otel

// SetupMetrics is the metrics sibling of SetupTelemetry — wires the
// ext/otel MeterProvider adapter against an OTLP / stdout / Noop
// pipeline so example servers emit the canonical MCP metrics that
// `server/metrics_middleware.go` records via the issue 7 MeterProvider
// seam. With the local `docker/observability/` stack running, the
// docker-provisioned Mimir lane lights up.
//
// Issue 668 (metrics half — paired with PR 735's library work).

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/panyam/mcpkit/core"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// MeterInstrumentationName is the OTel instrumentation library label
// stamped on every metric instrument when the caller has not
// overridden it. Matches the trace-side / logs-side server defaults
// so backends group server-emitted spans + logs + metrics under one
// library.
const MeterInstrumentationName = "github.com/panyam/mcpkit/server"

// metricsPeriodicReaderInterval is the periodic export interval used
// in stdout / otlp / auto modes. Demos want responsive panels; 5s
// matches Mimir's default scrape cadence and the dashboard's 30s
// query window so a manual `tools/call` shows up within one panel
// refresh.
const metricsPeriodicReaderInterval = 5 * time.Second

// SetupMetrics constructs a core.MeterProvider per the same decision
// matrix as SetupTelemetry (see setup.go). The OTLP-bearing modes
// install the OTel MeterProvider adapter so the server's metrics
// middleware emits through a real SDK pipeline; the seam keeps the
// caller's call site identical across modes.
//
// Exporter selector × runtime behavior:
//
//	Exporter==""       → core.NoopMeterProvider{}. Zero allocations,
//	                     no SDK pulled at runtime. Server's metrics
//	                     middleware short-circuits on the Noop
//	                     install gate.
//	Exporter=="stdout" → stdoutmetric exporter wrapped in a periodic
//	                     reader (5s interval). Demo-friendly: every
//	                     interval dumps the current instrument values
//	                     as JSON on the configured writer. Pretty
//	                     output is enabled.
//	Exporter=="otlp"   → otlpmetricgrpc exporter to OTLPEndpoint
//	                     (default localhost:4317), wrapped in a
//	                     periodic reader. Dial-failure logs a warning
//	                     and falls back to Noop — a dead collector
//	                     does not break `make demo`.
//	Exporter=="auto"   → same as "otlp" but silent on dial-failure
//	                     (the operator explicitly opted into
//	                     maybe-on-maybe-off semantics).
//
// Resource: stdout / otlp / auto modes attach the resolved Resource
// (service.name + WithResourceAttr extras) so Mimir labels are useful
// out of the box — pairs symmetrically with the trace / logs sides.
//
// Trace ↔ metrics pivot: the ext/otel meter adapter forwards every
// Add / Record ctx unchanged, and the OTel SDK's default exemplar
// filter (AlwaysOnSampleParent) reads the active span via the OTel
// ctx accessor to stamp an exemplar on the measurement. Grafana +
// Mimir surface these as clickable dots that jump to the matching
// Tempo trace — closes the loop the SEP-414 work + issue 7 metrics
// seam set up.
//
// Shutdown contract mirrors SetupTelemetry: defer the returned
// ShutdownFunc so the periodic reader flushes one final batch before
// the process exits. Calling shutdown twice is safe.
func SetupMetrics(ctx context.Context, opts ...SetupOption) (core.MeterProvider, ShutdownFunc, error) {
	cfg := setupConfig{stdoutWriter: os.Stdout}
	for _, opt := range opts {
		opt(&cfg)
	}
	applyEnvFallbacks(&cfg)

	switch cfg.exporter {
	case "":
		return core.NoopMeterProvider{}, noopShutdown, nil

	case ExporterStdout:
		exp, err := stdoutmetric.New(
			stdoutmetric.WithWriter(cfg.stdoutWriter),
			stdoutmetric.WithPrettyPrint(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("commonotel.SetupMetrics stdout exporter: %w", err)
		}
		mp, shutdown := buildMeterProvider(exp, &cfg)
		return mp, shutdown, nil

	case ExporterOTLP:
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			log.Printf("commonotel.SetupMetrics: OTLP endpoint %s unreachable (%v) — falling back to Noop. Bring up docker/observability/ to enable metrics, or set EXPORTER='' to silence this warning.", cfg.otlpEndpoint, err)
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		exp, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(cfg.otlpEndpoint),
			otlpmetricgrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("commonotel.SetupMetrics: OTLP exporter init failed (%v) — falling back to Noop.", err)
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		mp, shutdown := buildMeterProvider(exp, &cfg)
		return mp, shutdown, nil

	case ExporterAuto:
		// Silent fallback: the operator opted into maybe-on-maybe-off
		// semantics. A failed exporter init (separate from dial
		// reachability — e.g. invalid endpoint format) still logs
		// because that's a configuration error, not an environment
		// state.
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		exp, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(cfg.otlpEndpoint),
			otlpmetricgrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("commonotel.SetupMetrics (auto): OTLP exporter init failed (%v) — falling back to Noop.", err)
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		mp, shutdown := buildMeterProvider(exp, &cfg)
		return mp, shutdown, nil

	default:
		return nil, nil, fmt.Errorf("commonotel.SetupMetrics: unknown exporter %q (expected %q, %q, %q, or empty)", cfg.exporter, ExporterStdout, ExporterOTLP, ExporterAuto)
	}
}

// buildMeterProvider wraps the supplied metric exporter in an SDK
// MeterProvider with the resolved Resource and a periodic reader
// (5s interval — see metricsPeriodicReaderInterval), then hands the
// SDK provider to the ext/otel adapter so the dispatch path sees a
// dependency-free core.MeterProvider at the call site.
//
// The instrumentation library name follows the same precedence as
// the trace adapter: explicit WithInstrumentationName option wins,
// otherwise MeterInstrumentationName.
func buildMeterProvider(exp sdkmetric.Exporter, cfg *setupConfig) (core.MeterProvider, ShutdownFunc) {
	res := buildResource(cfg)
	sdkMP := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp,
			sdkmetric.WithInterval(metricsPeriodicReaderInterval),
		)),
	)
	shutdown := func(ctx context.Context) error {
		return sdkMP.Shutdown(ctx)
	}
	instrumentation := cfg.instrumentationName
	if instrumentation == "" {
		instrumentation = MeterInstrumentationName
	}
	return mcpotel.NewMeterProvider(sdkMP, mcpotel.WithMeterInstrumentationName(instrumentation)), shutdown
}

// SetupClientMetrics is SetupMetrics pre-configured for the host
// (client) side of an example walkthrough. Equivalent to
// SetupMetrics(ctx, opts..., WithInstrumentationName(ClientInstrumentationName))
// — saves walkthroughs from typing the magic string. Explicit
// WithInstrumentationName(...) in opts still wins (last option
// applied).
//
// Client-side metrics are emitted only when the client library
// itself instruments dispatch (currently scoped to telemetry middleware
// in `client/`); the helper exists for symmetry with the trace /
// logs sides and future client-side metric expansion.
func SetupClientMetrics(ctx context.Context, opts ...SetupOption) (core.MeterProvider, ShutdownFunc, error) {
	all := make([]SetupOption, 0, len(opts)+1)
	all = append(all, WithInstrumentationName(ClientInstrumentationName))
	all = append(all, opts...)
	return SetupMetrics(ctx, all...)
}
