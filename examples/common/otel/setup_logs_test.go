package otel_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"

	commonotel "github.com/panyam/mcpkit/examples/common/otel"
)

func TestSetupLogs_EmptyExporter_ReturnsSlogDefault(t *testing.T) {
	logger, shutdown, err := commonotel.SetupLogs(context.Background())
	if err != nil {
		t.Fatalf("SetupLogs: %v", err)
	}
	if logger == nil {
		t.Fatalf("SetupLogs must return a non-nil *slog.Logger even on the Noop path")
	}
	if logger != slog.Default() {
		t.Fatalf("Exporter=\"\" must return slog.Default(); got a different *slog.Logger")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown on Noop path must be no-op; got %v", err)
	}
}

func TestSetupLogs_UnknownExporter_ReturnsError(t *testing.T) {
	logger, shutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter("bogus-mode"),
	)
	if err == nil {
		t.Fatalf("unknown exporter must error so a typo doesn't silently turn logs off")
	}
	if logger != nil || shutdown != nil {
		t.Fatalf("error return must hand back nil logger + nil shutdown; got logger=%v shutdown=%v", logger, shutdown)
	}
	if !strings.Contains(err.Error(), "bogus-mode") {
		t.Fatalf("error message should name the bad exporter; got %v", err)
	}
}

func TestSetupLogs_Stdout_EmitsRecordToWriter(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	logger, shutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter(commonotel.ExporterStdout),
		commonotel.WithStdoutWriter(w),
		commonotel.WithServiceName("setup-logs-test-stdout"),
	)
	if err != nil {
		t.Fatalf("SetupLogs: %v", err)
	}

	logger.InfoContext(context.Background(), "test-log-record", "key", "value")

	// Shutdown flushes the BatchProcessor synchronously so the record
	// lands in the pipe before we read.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = w.Close()

	data, err := readAllFromPipe(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "test-log-record") {
		t.Fatalf("stdoutlog output missing record body; got: %s", out)
	}
	if !strings.Contains(out, "setup-logs-test-stdout") {
		t.Fatalf("stdoutlog output missing service.name Resource attribute; got: %s", out)
	}
}

func TestSetupLogs_OTLP_DialFailure_FallsBackToSlogDefault(t *testing.T) {
	closed := findClosedPortForLogs(t)

	logger, shutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter(commonotel.ExporterOTLP),
		commonotel.WithOTLPEndpoint(closed),
		commonotel.WithServiceName("setup-logs-test-otlp-fallback"),
	)
	if err != nil {
		t.Fatalf("dial failure must not error — the contract is graceful fallback; got %v", err)
	}
	if logger != slog.Default() {
		t.Fatalf("OTLP dial failure must fall back to slog.Default(); got a different *slog.Logger")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("fallback shutdown must be no-op; got %v", err)
	}
}

func TestSetupLogs_OTLP_ReachableEndpoint_BuildsOTelBackedLogger(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	logger, shutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter(commonotel.ExporterOTLP),
		commonotel.WithOTLPEndpoint(ln.Addr().String()),
		commonotel.WithServiceName("setup-logs-test-otlp-reachable"),
	)
	if err != nil {
		t.Fatalf("SetupLogs: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if logger == nil {
		t.Fatalf("reachable OTLP endpoint must produce a non-nil logger")
	}
	if logger == slog.Default() {
		t.Fatalf("reachable OTLP endpoint must NOT fall back to slog.Default — got the package default")
	}
}

func TestSetupLogs_Auto_UnreachableEndpoint_SilentFallback(t *testing.T) {
	closed := findClosedPortForLogs(t)

	logger, shutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter(commonotel.ExporterAuto),
		commonotel.WithOTLPEndpoint(closed),
		commonotel.WithServiceName("setup-logs-test-auto-unreachable"),
	)
	if err != nil {
		t.Fatalf("auto mode must not error on unreachable endpoint; got %v", err)
	}
	if logger != slog.Default() {
		t.Fatalf("auto mode + unreachable endpoint must return slog.Default(); got a different *slog.Logger")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("fallback shutdown must be no-op; got %v", err)
	}
}

func TestSetupLogs_Auto_ReachableEndpoint_BuildsOTelBackedLogger(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	logger, shutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter(commonotel.ExporterAuto),
		commonotel.WithOTLPEndpoint(ln.Addr().String()),
		commonotel.WithServiceName("setup-logs-test-auto-reachable"),
	)
	if err != nil {
		t.Fatalf("SetupLogs: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if logger == nil {
		t.Fatalf("reachable auto endpoint must produce a non-nil logger")
	}
	if logger == slog.Default() {
		t.Fatalf("reachable auto endpoint must NOT fall back to slog.Default")
	}
}

func TestSetupLogs_OTLPEndpointEnv_FallbackHonored(t *testing.T) {
	closed := findClosedPortForLogs(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", closed)

	// Same contract setup_test.go relies on for SetupTelemetry: with
	// no explicit endpoint, the env var feeds the probe. Closed port
	// drives the Noop / slog.Default fallback; if the env var were
	// ignored, the probe would target DefaultOTLPEndpoint
	// (localhost:4317) and the test result would depend on whether
	// anything is listening there, making this flaky in CI.
	logger, shutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter(commonotel.ExporterOTLP),
		commonotel.WithServiceName("setup-logs-test-env-fallback"),
	)
	if err != nil {
		t.Fatalf("SetupLogs: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if logger != slog.Default() {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT pointing at a closed port must drive slog.Default fallback")
	}
}

// findClosedPortForLogs mirrors the setup_test.go helper. Named
// distinctly so it does not collide if the two test files share a
// package — Go's external test packages allow same-named helpers
// per file, but the distinct name documents which file each call
// site reads from.
func findClosedPortForLogs(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// readAllFromPipe drains an os.Pipe read end into a buffer. Pulled
// out of the stdout-emission test because stdoutlog's BatchProcessor
// flushes asynchronously — but Shutdown is synchronous, so callers
// must call shutdown + Close(w) before invoking this helper.
func readAllFromPipe(r *os.File) ([]byte, error) {
	var buf bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			// io.EOF check via string isn't great, but this helper
			// stays inside the test file and the alternative is
			// dragging in io.EOF for two characters of savings.
			break
		}
	}
	return buf.Bytes(), nil
}
