package main

// SetupMeter is the metrics sibling of SetupTelemetry and SetupLogs —
// wires an OTel MeterProvider so the agent Runner's issue-1023
// instruments (agent.turns / agent.turn.duration / agent.steps /
// agent.tokens / agent.tool.calls / agent.tool.duration) export to the
// configured collector (then to Mimir/Prometheus and the mcpkit-agent
// Grafana dashboard). See telemetry_setup.go for the shared decision
// matrix (exporter selector, env fallbacks, endpoint probe, Resource).

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/panyam/mcpkit/core"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
)

// SetupMeter constructs a core.MeterProvider per the same decision
// matrix as SetupTelemetry (see telemetry_setup.go). The OTLP-bearing
// modes install a PeriodicReader so measurements flush on interval and
// on shutdown; the "" mode returns core.NoopMeterProvider so the
// unconfigured path allocates nothing and the deferred ShutdownFunc
// costs nothing.
//
// Exporter selector × runtime behavior mirrors the trace side:
//
//	Exporter==""       → core.NoopMeterProvider{} + no-op shutdown.
//	Exporter=="stdout" → stdoutmetric exporter → sdkmetric.MeterProvider.
//	                     Demo / teaching mode (measurements print as JSON).
//	Exporter=="otlp"   → otlpmetricgrpc exporter → sdkmetric.MeterProvider.
//	                     Dial-failure logs a warning and falls back to
//	                     Noop so a dead docker/observability/ stack never
//	                     breaks the run.
//	Exporter=="auto"   → same as "otlp" but silent on dial-failure.
//
// Shutdown drains the reader (final flush) before process exit; calling
// it twice is safe.
func SetupMeter(ctx context.Context, opts ...SetupOption) (core.MeterProvider, ShutdownFunc, error) {
	cfg := setupConfig{stdoutWriter: os.Stdout}
	for _, opt := range opts {
		opt(&cfg)
	}
	applyEnvFallbacks(&cfg)

	switch cfg.exporter {
	case "":
		return core.NoopMeterProvider{}, noopShutdown, nil

	case ExporterStdout:
		exp, err := stdoutmetric.New()
		if err != nil {
			return nil, nil, fmt.Errorf("SetupMeter stdout exporter: %w", err)
		}
		mp, shutdown := buildMeterProvider(sdkmetric.NewPeriodicReader(exp), &cfg)
		return mp, shutdown, nil

	case ExporterOTLP:
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			log.Printf("SetupMeter: OTLP endpoint %s unreachable (%v) — falling back to Noop. Bring up docker/observability/ to enable metrics, or set EXPORTER='' to silence this warning.", cfg.otlpEndpoint, err)
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		exp, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(cfg.otlpEndpoint),
			otlpmetricgrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupMeter: OTLP exporter init failed (%v) — falling back to Noop.", err)
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		mp, shutdown := buildMeterProvider(sdkmetric.NewPeriodicReader(exp), &cfg)
		return mp, shutdown, nil

	case ExporterAuto:
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		exp, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(cfg.otlpEndpoint),
			otlpmetricgrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupMeter (auto): OTLP exporter init failed (%v) — falling back to Noop.", err)
			return core.NoopMeterProvider{}, noopShutdown, nil
		}
		mp, shutdown := buildMeterProvider(sdkmetric.NewPeriodicReader(exp), &cfg)
		return mp, shutdown, nil

	default:
		return nil, nil, fmt.Errorf("SetupMeter: unknown exporter %q (expected %q, %q, %q, or empty)", cfg.exporter, ExporterStdout, ExporterOTLP, ExporterAuto)
	}
}

// buildMeterProvider wraps the supplied reader in an SDK MeterProvider
// with the resolved Resource (service.name + extras) and returns the
// mcpotel-wrapped core.MeterProvider + a ShutdownFunc that flushes and
// tears down the SDK side. The instrumentation library name follows the
// trace/logs precedence: explicit WithInstrumentationName wins.
func buildMeterProvider(reader sdkmetric.Reader, cfg *setupConfig) (core.MeterProvider, ShutdownFunc) {
	res := buildResource(cfg)
	sdkMP := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
	shutdown := func(ctx context.Context) error {
		return sdkMP.Shutdown(ctx)
	}
	var providerOpts []mcpotel.MeterOption
	if cfg.instrumentationName != "" {
		providerOpts = append(providerOpts, mcpotel.WithMeterInstrumentationName(cfg.instrumentationName))
	}
	return mcpotel.NewMeterProvider(sdkMP, providerOpts...), shutdown
}
