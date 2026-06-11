package otel_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
)

func TestSetupMetrics_EmptyExporter_ReturnsNoop(t *testing.T) {
	mp, shutdown, err := commonotel.SetupMetrics(context.Background())
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	if _, ok := mp.(core.NoopMeterProvider); !ok {
		t.Fatalf("Exporter=\"\" must return core.NoopMeterProvider; got %T", mp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown on Noop path must be no-op; got %v", err)
	}
}

func TestSetupMetrics_UnknownExporter_ReturnsError(t *testing.T) {
	mp, shutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter("bogus-mode"),
	)
	if err == nil {
		t.Fatalf("unknown exporter must error so a typo doesn't silently turn metrics off")
	}
	if mp != nil || shutdown != nil {
		t.Fatalf("error return must hand back nil mp + nil shutdown; got mp=%v shutdown=%v", mp, shutdown)
	}
	if !strings.Contains(err.Error(), "bogus-mode") {
		t.Fatalf("error message should name the bad exporter; got %v", err)
	}
}

func TestSetupMetrics_Stdout_BuildsSDKBackedProvider(t *testing.T) {
	// stdoutmetric exports on a periodic timer (5s default). We can't
	// reasonably block the test waiting for the periodic flush, so
	// the assertion is structural: SetupMetrics(stdout) must NOT
	// return Noop and shutdown must succeed (it triggers a
	// synchronous final flush).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	mp, shutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter(commonotel.ExporterStdout),
		commonotel.WithStdoutWriter(w),
		commonotel.WithServiceName("setup-metrics-test-stdout"),
	)
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	if _, ok := mp.(core.NoopMeterProvider); ok {
		t.Fatalf("Exporter=stdout must NOT return Noop; got NoopMeterProvider")
	}
	if mp == nil {
		t.Fatalf("SetupMetrics returned nil MeterProvider on stdout mode")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestSetupMetrics_OTLP_DialFailure_FallsBackToNoop(t *testing.T) {
	closed := findClosedPortForMetrics(t)

	mp, shutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter(commonotel.ExporterOTLP),
		commonotel.WithOTLPEndpoint(closed),
		commonotel.WithServiceName("setup-metrics-test-otlp-fallback"),
	)
	if err != nil {
		t.Fatalf("dial failure must not error — graceful Noop fallback is the contract; got %v", err)
	}
	if _, ok := mp.(core.NoopMeterProvider); !ok {
		t.Fatalf("OTLP dial failure must fall back to NoopMeterProvider; got %T", mp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("fallback shutdown must be no-op; got %v", err)
	}
}

func TestSetupMetrics_OTLP_ReachableEndpoint_BuildsSDKBackedProvider(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	mp, shutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter(commonotel.ExporterOTLP),
		commonotel.WithOTLPEndpoint(ln.Addr().String()),
		commonotel.WithServiceName("setup-metrics-test-otlp-reachable"),
	)
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if _, ok := mp.(core.NoopMeterProvider); ok {
		t.Fatalf("reachable OTLP endpoint must NOT fall back to Noop; got NoopMeterProvider")
	}
	if mp == nil {
		t.Fatalf("SetupMetrics returned nil MeterProvider on reachable endpoint")
	}
}

func TestSetupMetrics_Auto_UnreachableEndpoint_SilentNoop(t *testing.T) {
	closed := findClosedPortForMetrics(t)

	mp, shutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter(commonotel.ExporterAuto),
		commonotel.WithOTLPEndpoint(closed),
		commonotel.WithServiceName("setup-metrics-test-auto-unreachable"),
	)
	if err != nil {
		t.Fatalf("auto mode must not error on unreachable endpoint; got %v", err)
	}
	if _, ok := mp.(core.NoopMeterProvider); !ok {
		t.Fatalf("auto + unreachable endpoint must return Noop; got %T", mp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("fallback shutdown must be no-op; got %v", err)
	}
}

func TestSetupMetrics_Auto_ReachableEndpoint_UsesSDK(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	mp, shutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter(commonotel.ExporterAuto),
		commonotel.WithOTLPEndpoint(ln.Addr().String()),
		commonotel.WithServiceName("setup-metrics-test-auto-reachable"),
	)
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if _, ok := mp.(core.NoopMeterProvider); ok {
		t.Fatalf("reachable auto endpoint must NOT fall back to Noop")
	}
	if mp == nil {
		t.Fatalf("SetupMetrics returned nil MeterProvider on reachable auto endpoint")
	}
}

func TestSetupMetrics_OTLPEndpointEnv_FallbackHonored(t *testing.T) {
	closed := findClosedPortForMetrics(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", closed)

	// Same env-precedence contract setup_test.go / setup_logs_test.go
	// pin: with no explicit endpoint, the env var feeds the probe.
	mp, shutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter(commonotel.ExporterOTLP),
		commonotel.WithServiceName("setup-metrics-test-env-fallback"),
	)
	if err != nil {
		t.Fatalf("SetupMetrics: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if _, ok := mp.(core.NoopMeterProvider); !ok {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT pointing at a closed port must drive Noop fallback; got %T", mp)
	}
}

// findClosedPortForMetrics mirrors the same-named helpers in
// setup_test.go and setup_logs_test.go. Distinct name documents which
// file each call site reads from when the tests are split across
// files.
func findClosedPortForMetrics(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
