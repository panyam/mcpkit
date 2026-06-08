// Package otel collects the OpenTelemetry pipeline boilerplate that
// every mcpkit example with `--exporter=stdout|otlp` support needs:
// dispatching between exporter modes, building an SDK TracerProvider
// with the right service.name + sync exporting via the
// `mcpotel.NewTracerProvider` helper, and tidying up the shutdown
// closure on exit.
//
// Why this lives in examples/common/otel/ rather than each example
// re-implementing it: the helpers are surface-agnostic. The first
// consumer is examples/otel/stdout/; the SEP-414 P6 surface examples
// (issue 658 ext/auth, 659 ext/tasks, 660 ext/ui, 664 reverse-call)
// will all want the same shape when they add their own OTel wiring.
// Centralising here avoids the copy-paste-and-drift loop.
//
// Why NOT entirely in ext/otel/: the exporter construction is demo-
// specific (which exporter library, stdouttrace vs otlptracegrpc).
// ext/otel ships the TracerProvider helper (`NewTracerProvider`)
// that this file consumes; the per-exporter construction stays here.
package otel

import (
	"context"
	"fmt"
	"os"

	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Exporter names. These are what examples spell on the command line —
// `--exporter=stdout` or `--exporter=otlp`.
const (
	ExporterStdout = "stdout"
	ExporterOTLP   = "otlp"

	// DefaultOTLPEndpoint matches the gRPC receiver port that
	// docker/observability/docker-compose.yml exposes. The OTel Go
	// SDK's own default endpoint convention is also :4317, so this
	// is the lowest-friction default for both the docker stack and
	// any other local OTLP receiver an operator may be running.
	DefaultOTLPEndpoint = "localhost:4317"
)

// BuildPipeline dispatches to the right exporter-mode constructor.
// Returns the TracerProvider + flush-on-exit closure + error. The
// returned TracerProvider is ready to pass to
// mcpotel.NewProvider(...) for wiring into server.WithTracerProvider
// or client.WithTracerProvider.
//
// stdoutWriter is consumed only by the stdout-mode path; pass
// os.Stdout for the typical "spans dump to terminal" demo.
// otlpEndpoint is consumed only by the OTLP-mode path; pass
// DefaultOTLPEndpoint to point at the docker/observability/ stack.
//
// Unknown exporter strings produce a typed error so callers can
// surface a user-friendly message — log.Fatalf is left to the
// caller's main().
func BuildPipeline(exporter, otlpEndpoint, serviceName string, stdoutWriter *os.File) (*sdktrace.TracerProvider, func(), error) {
	switch exporter {
	case ExporterStdout:
		return NewStdoutPipeline(stdoutWriter, serviceName)
	case ExporterOTLP:
		return NewOTLPPipeline(otlpEndpoint, serviceName)
	default:
		return nil, nil, fmt.Errorf("unknown exporter %q: expected %q or %q", exporter, ExporterStdout, ExporterOTLP)
	}
}

// NewStdoutPipeline constructs an SDK TracerProvider that writes
// spans to the supplied writer via the stdouttrace exporter, with
// the supplied serviceName baked into the Resource. The returned
// shutdown closure flushes the exporter; defer it so buffered spans
// land on stdout before the process exits.
//
// Uses sync exporting via mcpotel.WithSyncer — for stdout output,
// every span renders as the operator watches the terminal. Batched
// stdout would make the demo's small burst of spans clump together
// in confusing order.
func NewStdoutPipeline(w *os.File, serviceName string) (*sdktrace.TracerProvider, func(), error) {
	exp, err := stdouttrace.New(
		stdouttrace.WithWriter(w),
		stdouttrace.WithPrettyPrint(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("stdouttrace.New: %w", err)
	}
	tp := mcpotel.NewTracerProvider(exp,
		mcpotel.WithServiceName(serviceName),
		mcpotel.WithSyncer(),
	)
	shutdown := func() { _ = tp.Shutdown(context.Background()) }
	return tp, shutdown, nil
}

// NewOTLPPipeline constructs an SDK TracerProvider that ships spans
// via OTLP gRPC to the supplied endpoint (typically the
// docker/observability/ collector at localhost:4317). The connection
// is configured insecure — the docker stack uses no TLS, on the
// assumption you're running it locally for development. Real
// production deployments would supply credentials and a non-insecure
// endpoint via the standard OTel env vars.
//
// Uses sync exporting (via mcpotel.WithSyncer) so the demo doesn't
// hit the batch-flush foot-gun where the operator's SIGTERM
// terminates the process before the batch processor pushes its
// queue. Real high-throughput servers should compose
// mcpotel.NewTracerProvider directly (without WithSyncer) and handle
// SIGTERM with explicit ForceFlush + Shutdown.
func NewOTLPPipeline(endpoint, serviceName string) (*sdktrace.TracerProvider, func(), error) {
	exp, err := otlptracegrpc.New(
		context.Background(),
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("otlptracegrpc.New: %w", err)
	}
	tp := mcpotel.NewTracerProvider(exp,
		mcpotel.WithServiceName(serviceName),
		mcpotel.WithSyncer(),
	)
	shutdown := func() {
		shutdownCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = tp.Shutdown(shutdownCtx)
	}
	return tp, shutdown, nil
}
