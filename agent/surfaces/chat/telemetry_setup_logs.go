package main

// SetupLogs is the logs sibling of SetupTelemetry — wires the
// otelslog bridge into an OTel LoggerProvider so handlers that log
// via slog.*Context(ctx, ...) ship trace-correlated records to the
// configured collector (then to Loki via the docker/observability/
// stack). See setup.go for the shared decision matrix.
//
// Issue 668 (logs half — no library seam needed; stdlib slog is the
// seam per the modernized issue 7).

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// LoggerInstrumentationName is the OTel instrumentation library
// label the otelslog bridge stamps on every emitted log record when
// the caller has not overridden it via WithInstrumentationName.
// Matches the trace-side server default (mcpotel.defaultInstrumentationName)
// so backends group server-emitted spans + logs under one library.
const LoggerInstrumentationName = "github.com/panyam/mcpkit/server"

// SetupLogs constructs an *slog.Logger per the same decision matrix
// as SetupTelemetry (see setup.go). The OTLP-bearing modes install
// the OTel otelslog bridge so log records flow through an OTel
// LoggerProvider to the configured collector with trace correlation
// stamped automatically.
//
// Exporter selector × runtime behavior:
//
//	Exporter==""       → slog.Default() (stdlib). Zero deps pulled
//	                     at runtime, no OTLP side, no SDK lifecycle.
//	                     The returned ShutdownFunc is a no-op so the
//	                     caller's deferred call costs nothing.
//	Exporter=="stdout" → stdoutlog exporter → sdklog.LoggerProvider
//	                     + otelslog bridge. Records pretty-print as
//	                     JSON on the configured writer (default
//	                     os.Stdout). Teaching / demo mode.
//	Exporter=="otlp"   → otlploggrpc exporter → sdklog.LoggerProvider
//	                     + otelslog bridge. Dial-failure logs a
//	                     warning and falls back to slog.Default() so
//	                     a dead docker/observability/ stack never
//	                     breaks `make demo`.
//	Exporter=="auto"   → same as "otlp" but silent on dial-failure
//	                     (the operator explicitly opted into
//	                     maybe-on-maybe-off semantics).
//
// Trace correlation: the otelslog bridge reads the active span via
// OTel's own ctx accessor (the same ctx key ext/otel.Provider's
// tracer.Start installs) and stamps trace_id + span_id onto every
// record — but ONLY when handlers log via slog.*Context(ctx, ...).
// Handlers that call slog.Info(...) without ctx lose the pivot.
// Grafana's Loki datasource (auto-provisioned with a `traceID`
// derived field per docker/observability/grafana/) renders the
// correlation as a clickable link back to Tempo.
//
// Resource: stdout/otlp/auto modes attach the resolved Resource
// (service.name + WithResourceAttr extras) so Loki labels are
// useful out of the box. The "" mode has no Resource — the caller's
// slog.Default() does not get its Resource rewritten.
//
// Coexistence with MCP `notifications/message` logs: server-side
// observability (this helper) and client-visible MCP logs
// (core.MCPLogHandler) emit through distinct slog handlers and do
// not interfere. A server can compose both by attaching multiple
// handlers via slogmulti or by emitting through two separate
// loggers.
//
// Shutdown contract mirrors SetupTelemetry: defer the returned
// ShutdownFunc so the BatchProcessor flushes buffered records
// before the process exits. Calling shutdown twice is safe.
func SetupLogs(ctx context.Context, opts ...SetupOption) (*slog.Logger, ShutdownFunc, error) {
	cfg := setupConfig{stdoutWriter: os.Stdout}
	for _, opt := range opts {
		opt(&cfg)
	}
	applyEnvFallbacks(&cfg)

	switch cfg.exporter {
	case "":
		return slog.Default(), noopShutdown, nil

	case ExporterStdout:
		exp, err := stdoutlog.New(
			stdoutlog.WithWriter(cfg.stdoutWriter),
			stdoutlog.WithPrettyPrint(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("SetupLogs stdout exporter: %w", err)
		}
		logger, shutdown := buildLogger(exp, &cfg)
		return logger, shutdown, nil

	case ExporterOTLP:
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			log.Printf("SetupLogs: OTLP endpoint %s unreachable (%v) — falling back to slog.Default. Bring up docker/observability/ to enable trace-correlated logs, or set EXPORTER='' to silence this warning.", cfg.otlpEndpoint, err)
			return slog.Default(), noopShutdown, nil
		}
		exp, err := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(cfg.otlpEndpoint),
			otlploggrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupLogs: OTLP exporter init failed (%v) — falling back to slog.Default.", err)
			return slog.Default(), noopShutdown, nil
		}
		logger, shutdown := buildLogger(exp, &cfg)
		return logger, shutdown, nil

	case ExporterAuto:
		// Silent fallback: the operator opted into maybe-on-maybe-off
		// semantics, so a missing stack is not surprising. A failed
		// exporter init (separate from dial reachability — e.g.
		// invalid endpoint format) still logs because that's a
		// configuration error, not an environment state.
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			return slog.Default(), noopShutdown, nil
		}
		exp, err := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(cfg.otlpEndpoint),
			otlploggrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupLogs (auto): OTLP exporter init failed (%v) — falling back to slog.Default.", err)
			return slog.Default(), noopShutdown, nil
		}
		logger, shutdown := buildLogger(exp, &cfg)
		return logger, shutdown, nil

	default:
		return nil, nil, fmt.Errorf("SetupLogs: unknown exporter %q (expected %q, %q, %q, or empty)", cfg.exporter, ExporterStdout, ExporterOTLP, ExporterAuto)
	}
}

// buildLogger wraps the supplied log exporter in an SDK
// LoggerProvider with the resolved Resource (service.name + extras),
// installs a BatchProcessor (records flush on shutdown or batch
// boundary — appropriate for OTLP; the stdout path also batches so
// demo writes don't race the process exit), and returns an
// otelslog-bridged *slog.Logger + the shutdown closer that drains
// the SDK pipeline.
//
// The instrumentation library name follows the same precedence as
// the trace adapter: explicit WithInstrumentationName option wins,
// otherwise LoggerInstrumentationName.
func buildLogger(exp sdklog.Exporter, cfg *setupConfig) (*slog.Logger, ShutdownFunc) {
	res := buildResource(cfg)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	shutdown := func(ctx context.Context) error {
		return lp.Shutdown(ctx)
	}
	instrumentation := cfg.instrumentationName
	if instrumentation == "" {
		instrumentation = LoggerInstrumentationName
	}
	handler := otelslog.NewHandler(instrumentation, otelslog.WithLoggerProvider(lp))
	return slog.New(handler), shutdown
}

// SetupClientLogs is SetupLogs pre-configured for the host (client)
// side of an example walkthrough. Equivalent to
// SetupLogs(ctx, opts..., WithInstrumentationName(ClientInstrumentationName))
// — saves every walkthrough from typing the magic string. Explicit
// WithInstrumentationName(...) in opts still wins (last option
// applied).
//
// Pair the returned *slog.Logger with slog.SetDefault(logger) (or
// thread it explicitly) so subsequent slog.*Context(ctx, ...) calls
// on the client side ship to OTLP alongside the host's trace spans.
func SetupClientLogs(ctx context.Context, opts ...SetupOption) (*slog.Logger, ShutdownFunc, error) {
	all := make([]SetupOption, 0, len(opts)+1)
	all = append(all, WithInstrumentationName(ClientInstrumentationName))
	all = append(all, opts...)
	return SetupLogs(ctx, all...)
}
