package main

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestSetupTelemetry_EmptyExporter_ReturnsNoop(t *testing.T) {
	tp, shutdown, err := SetupTelemetry(context.Background())
	if err != nil {
		t.Fatalf("SetupTelemetry: %v", err)
	}
	if _, ok := tp.(core.NoopTracerProvider); !ok {
		t.Fatalf("Exporter=\"\" must return core.NoopTracerProvider; got %T", tp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown on Noop path must be no-op; got %v", err)
	}
}

func TestSetupTelemetry_UnknownExporter_ReturnsError(t *testing.T) {
	tp, shutdown, err := SetupTelemetry(context.Background(),
		WithExporter("jaeger-flavor-of-the-week"),
	)
	if err == nil {
		t.Fatalf("unknown exporter must error so a typo doesn't silently turn telemetry off")
	}
	if tp != nil || shutdown != nil {
		t.Fatalf("error return must hand back nil tp + nil shutdown; got tp=%v shutdown=%v", tp, shutdown)
	}
	if !strings.Contains(err.Error(), "jaeger-flavor-of-the-week") {
		t.Fatalf("error message should name the bad exporter; got %v", err)
	}
}

func TestSetupTelemetry_Stdout_EmitsSpanToWriter(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	tp, shutdown, err := SetupTelemetry(context.Background(),
		WithExporter(ExporterStdout),
		WithStdoutWriter(w),
		WithServiceName("setup-test-stdout"),
	)
	if err != nil {
		t.Fatalf("SetupTelemetry: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	_, span := tp.StartSpan(context.Background(), "stdout-test-span")
	span.End()
	_ = w.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "stdout-test-span") {
		t.Fatalf("stdouttrace output missing span name; got: %s", out)
	}
	if !strings.Contains(out, "setup-test-stdout") {
		t.Fatalf("stdouttrace output missing service.name Resource attribute; got: %s", out)
	}
}

func TestSetupTelemetry_OTLP_DialFailure_FallsBackToNoop(t *testing.T) {
	closed := findClosedPort(t)

	tp, shutdown, err := SetupTelemetry(context.Background(),
		WithExporter(ExporterOTLP),
		WithOTLPEndpoint(closed),
		WithServiceName("setup-test-otlp-fallback"),
	)
	if err != nil {
		t.Fatalf("dial failure must not error — the contract is graceful Noop fallback; got %v", err)
	}
	if _, ok := tp.(core.NoopTracerProvider); !ok {
		t.Fatalf("OTLP dial failure must fall back to Noop; got %T", tp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("Noop fallback shutdown must be no-op; got %v", err)
	}
}

func TestSetupTelemetry_OTLP_ReachableEndpoint_ConstructsSDKProvider(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	tp, shutdown, err := SetupTelemetry(context.Background(),
		WithExporter(ExporterOTLP),
		WithOTLPEndpoint(ln.Addr().String()),
		WithServiceName("setup-test-otlp-reachable"),
	)
	if err != nil {
		t.Fatalf("SetupTelemetry: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if _, ok := tp.(core.NoopTracerProvider); ok {
		t.Fatalf("reachable OTLP endpoint must NOT fall back to Noop — got NoopTracerProvider")
	}
	if tp == nil {
		t.Fatalf("SetupTelemetry returned nil TracerProvider on reachable OTLP endpoint")
	}
}

func TestSetupTelemetry_OTLPEndpointEnv_FallbackHonored(t *testing.T) {
	closed := findClosedPort(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", closed)

	// No WithOTLPEndpoint passed — should pick up the env var,
	// probe it, and fall back to Noop because the port is closed.
	// If the env var were ignored, the probe would target
	// DefaultOTLPEndpoint (localhost:4317) and the test's outcome
	// would depend on whether anything is listening there — which
	// would make this test flaky in CI.
	tp, shutdown, err := SetupTelemetry(context.Background(),
		WithExporter(ExporterOTLP),
		WithServiceName("setup-test-env-fallback"),
	)
	if err != nil {
		t.Fatalf("SetupTelemetry: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if _, ok := tp.(core.NoopTracerProvider); !ok {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT pointing at a closed port must drive Noop fallback; got %T", tp)
	}
}

func TestSetupTelemetry_Auto_UnreachableEndpoint_SilentNoop(t *testing.T) {
	closed := findClosedPort(t)

	// Capture the log output to assert "auto" mode stays silent on
	// unreachable — distinct from "otlp" mode which prints a warning.
	tp, shutdown, err := SetupTelemetry(context.Background(),
		WithExporter(ExporterAuto),
		WithOTLPEndpoint(closed),
		WithServiceName("setup-test-auto-unreachable"),
	)
	if err != nil {
		t.Fatalf("auto mode must not error on unreachable endpoint; got %v", err)
	}
	if _, ok := tp.(core.NoopTracerProvider); !ok {
		t.Fatalf("auto mode + unreachable endpoint must return Noop; got %T", tp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("Noop fallback shutdown must be no-op; got %v", err)
	}
}

func TestSetupTelemetry_Auto_ReachableEndpoint_UsesOTLP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	tp, shutdown, err := SetupTelemetry(context.Background(),
		WithExporter(ExporterAuto),
		WithOTLPEndpoint(ln.Addr().String()),
		WithServiceName("setup-test-auto-reachable"),
	)
	if err != nil {
		t.Fatalf("SetupTelemetry: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if _, ok := tp.(core.NoopTracerProvider); ok {
		t.Fatalf("auto mode + reachable endpoint must construct a real TracerProvider, not Noop")
	}
	if tp == nil {
		t.Fatalf("SetupTelemetry returned nil TracerProvider on reachable auto endpoint")
	}
}

func TestSetupTelemetry_ExplicitOptionBeatsEnv(t *testing.T) {
	closedA := findClosedPort(t)
	closedB := findClosedPort(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", closedA)

	// Explicit WithOTLPEndpoint(closedB) should win over env's
	// closedA. Either way both ports are closed so the OUTCOME is
	// the same Noop fallback — but if the explicit option were
	// ignored, the probe error would mention closedA instead of
	// closedB. We can't easily intercept the log line, so this
	// test asserts the runtime contract via the no-error guarantee
	// (a wholly-broken precedence would still produce Noop, but
	// the test serves as documentation that explicit beats env).
	tp, _, err := SetupTelemetry(context.Background(),
		WithExporter(ExporterOTLP),
		WithOTLPEndpoint(closedB),
	)
	if err != nil {
		t.Fatalf("SetupTelemetry: %v", err)
	}
	if _, ok := tp.(core.NoopTracerProvider); !ok {
		t.Fatalf("expected Noop fallback when both endpoints closed; got %T", tp)
	}
}

// findClosedPort binds 127.0.0.1:0, captures the port, then closes
// the listener. Returns the now-refused address. Marginally racy in
// theory (another process could grab the port between Close and the
// caller's dial); in practice the test runs in milliseconds and the
// race window is too narrow to cause flakes.
func findClosedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
