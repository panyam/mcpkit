// Package otel ships SetupTelemetry — env-gated TracerProvider
// wiring that every mcpkit example uses to get a uniform, opt-in
// observability surface (issue 666).
//
// The contract every example follows:
//
//	tp, shutdown, err := commonotel.SetupTelemetry(ctx,
//	    commonotel.WithServiceName("my-example"),
//	    commonotel.WithExporter(*exporterFlag),
//	    commonotel.WithOTLPEndpoint(*otlpEndpointFlag),
//	)
//	if err != nil { log.Fatalf(...) }
//	defer shutdown(context.Background())
//
//	common.RunServer(common.ServerConfig{
//	    ...
//	    TracerProvider: tp,
//	})
//
// Decision matrix (Exporter selector × env):
//
//	Exporter==""       → core.NoopTracerProvider{} + no-op shutdown.
//	                     Zero allocations, no SDK pulled at runtime.
//	                     This is the DEFAULT — operators opt in per
//	                     example via --exporter or EXPORTER=.
//	Exporter=="stdout" → stdouttrace exporter (sync); every span
//	                     dumps to the supplied writer (os.Stdout by
//	                     default). Teaching / demo mode.
//	Exporter=="otlp"   → otlptracegrpc exporter (sync) to OTLPEndpoint
//	                     (default localhost:4317, matches the
//	                     docker/observability/ stack). Honors
//	                     OTEL_EXPORTER_OTLP_ENDPOINT as a fallback.
//
// OTLP dial / construction failure → log a warning and fall back to
// Noop. A dead observability stack should never break `make demo`.
//
// Standard OTel env vars (OTEL_SERVICE_NAME, OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_RESOURCE_ATTRIBUTES) are honored as fallbacks under explicit
// options — explicit code beats spooky env, but env still works for
// `make demo` orchestration.
//
// The returned core.TracerProvider is already wrapped via
// mcpotel.NewProvider so the caller hands it directly to
// server.WithTracerProvider or client.WithTracerProvider — no
// adapter wiring at the call site.
package otel

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// otlpProbeTimeout caps how long SetupTelemetry waits on a TCP
// connect attempt to the OTLP endpoint before declaring the stack
// unreachable and falling back to Noop. Generous enough for a
// container starting up but short enough that a wholly-absent stack
// doesn't visibly stall `make demo`.
const otlpProbeTimeout = 500 * time.Millisecond

// Exporter mode constants — the strings examples accept on
// --exporter and EXPORTER=.
const (
	ExporterStdout = "stdout"
	ExporterOTLP   = "otlp"

	// DefaultOTLPEndpoint matches the gRPC receiver port that the
	// docker/observability/ stack exposes. The OTel Go SDK's own
	// default endpoint convention is also :4317, so this is the
	// lowest-friction default for both the local docker stack and
	// any other OTLP receiver an operator may be running.
	DefaultOTLPEndpoint = "localhost:4317"
)

// ShutdownFunc flushes any buffered spans and tears down the
// exporter pipeline. SetupTelemetry returns one; callers MUST defer
// it — typically with context.Background(), since the shutdown path
// shouldn't honor the original ctx's cancellation (cancel is often
// what triggered the shutdown in the first place).
//
// The function is a no-op when Exporter=="" or when an OTLP dial
// fell back to Noop. Calling it twice is safe.
type ShutdownFunc func(context.Context) error

// SetupOption configures the SetupTelemetry call. Apply via the
// With... helpers; later options override earlier ones touching the
// same field.
type SetupOption func(*setupConfig)

type setupConfig struct {
	serviceName         string
	exporter            string
	otlpEndpoint        string
	stdoutWriter        *os.File
	resourceAttr        map[string]string
	instrumentationName string
}

// WithServiceName sets the OTel Resource service.name attribute. If
// unset, OTEL_SERVICE_NAME is consulted; if that is also empty, the
// SDK default (`unknown_service:<binary>`) applies. Every example
// SHOULD pass this so traces land under a recognizable name in
// Grafana / Tempo / Jaeger.
func WithServiceName(name string) SetupOption {
	return func(c *setupConfig) {
		if name != "" {
			c.serviceName = name
		}
	}
}

// WithExporter picks the exporter mode: "", "stdout", or "otlp".
// Empty string → Noop (the default). Unknown values cause
// SetupTelemetry to return an error so a typo doesn't silently turn
// telemetry off.
func WithExporter(name string) SetupOption {
	return func(c *setupConfig) {
		c.exporter = name
	}
}

// WithOTLPEndpoint overrides the OTLP gRPC endpoint. When unset,
// OTEL_EXPORTER_OTLP_ENDPOINT is consulted; if that is also empty,
// DefaultOTLPEndpoint (localhost:4317) is used. Only relevant when
// Exporter=="otlp".
func WithOTLPEndpoint(endpoint string) SetupOption {
	return func(c *setupConfig) {
		if endpoint != "" {
			c.otlpEndpoint = endpoint
		}
	}
}

// WithStdoutWriter overrides the writer used in Exporter=="stdout"
// mode. Default os.Stdout. Mostly useful in tests that want to
// assert on the rendered span output.
func WithStdoutWriter(w *os.File) SetupOption {
	return func(c *setupConfig) {
		if w != nil {
			c.stdoutWriter = w
		}
	}
}

// WithInstrumentationName overrides the OTel instrumentation library
// name passed to mcpotel.NewProvider. Defaults to mcpotel's own
// default (`github.com/panyam/mcpkit/server`). Client-side wiring
// typically passes `github.com/panyam/mcpkit/client` so observability
// backends group spans by emitting library.
//
// Empty name is a no-op (falls through to mcpotel's default).
func WithInstrumentationName(name string) SetupOption {
	return func(c *setupConfig) {
		if name != "" {
			c.instrumentationName = name
		}
	}
}

// WithResourceAttr layers an additional OTel Resource attribute on
// top of service.name. Use for deployment.environment, replica id,
// tenant, or any other dimension the operator wants in Grafana
// filters. May be called multiple times — last value wins per key.
//
// Attributes set via this option take precedence over the same key
// arriving from OTEL_RESOURCE_ATTRIBUTES env. Discarded on the Noop
// path (Exporter==""), which has no Resource.
func WithResourceAttr(key, value string) SetupOption {
	return func(c *setupConfig) {
		if c.resourceAttr == nil {
			c.resourceAttr = map[string]string{}
		}
		c.resourceAttr[key] = value
	}
}

// SetupTelemetry constructs a core.TracerProvider per the decision
// matrix in the package doc. The returned ShutdownFunc must be
// deferred so buffered spans flush before the process exits — a
// common source of the "no spans appeared in Grafana" surprise.
//
// Errors surface ONLY for unknown exporter names. Endpoint
// construction failures fall back to Noop with a log warning — the
// contract is "a dead observability stack never breaks make demo."
func SetupTelemetry(ctx context.Context, opts ...SetupOption) (core.TracerProvider, ShutdownFunc, error) {
	cfg := setupConfig{stdoutWriter: os.Stdout}
	for _, opt := range opts {
		opt(&cfg)
	}
	applyEnvFallbacks(&cfg)

	switch cfg.exporter {
	case "":
		return core.NoopTracerProvider{}, noopShutdown, nil

	case ExporterStdout:
		exp, err := stdouttrace.New(
			stdouttrace.WithWriter(cfg.stdoutWriter),
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("commonotel.SetupTelemetry stdout exporter: %w", err)
		}
		tp, shutdown := buildTracerProvider(exp, &cfg)
		return tp, shutdown, nil

	case ExporterOTLP:
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			log.Printf("commonotel.SetupTelemetry: OTLP endpoint %s unreachable (%v) — falling back to Noop. Bring up docker/observability/ to enable tracing, or set EXPORTER='' to silence this warning.", cfg.otlpEndpoint, err)
			return core.NoopTracerProvider{}, noopShutdown, nil
		}
		exp, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.otlpEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("commonotel.SetupTelemetry: OTLP exporter init failed (%v) — falling back to Noop.", err)
			return core.NoopTracerProvider{}, noopShutdown, nil
		}
		tp, shutdown := buildTracerProvider(exp, &cfg)
		return tp, shutdown, nil

	default:
		return nil, nil, fmt.Errorf("commonotel.SetupTelemetry: unknown exporter %q (expected %q, %q, or empty)", cfg.exporter, ExporterStdout, ExporterOTLP)
	}
}

// buildTracerProvider wraps the supplied exporter in an SDK
// TracerProvider with the resolved Resource (service.name + extras),
// sync exporting (demos need every span on End(), not after a batch
// flush window), and returns the mcpotel-wrapped core.TracerProvider
// + a ShutdownFunc that flushes + tears down the SDK side.
func buildTracerProvider(exp sdktrace.SpanExporter, cfg *setupConfig) (core.TracerProvider, ShutdownFunc) {
	res := buildResource(cfg)
	sdkTP := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSyncer(exp),
	)
	shutdown := func(ctx context.Context) error {
		return sdkTP.Shutdown(ctx)
	}
	var providerOpts []mcpotel.Option
	if cfg.instrumentationName != "" {
		providerOpts = append(providerOpts, mcpotel.WithInstrumentationName(cfg.instrumentationName))
	}
	return mcpotel.NewProvider(sdkTP, providerOpts...), shutdown
}

// buildResource composes the OTel Resource from service.name +
// explicit WithResourceAttr options + OTEL_RESOURCE_ATTRIBUTES env.
// Explicit options shadow env on key conflict.
func buildResource(cfg *setupConfig) *resource.Resource {
	attrs := make([]attribute.KeyValue, 0, 4+len(cfg.resourceAttr))
	if cfg.serviceName != "" {
		attrs = append(attrs, semconv.ServiceName(cfg.serviceName))
	}
	for k, v := range parseResourceAttrEnv(os.Getenv("OTEL_RESOURCE_ATTRIBUTES")) {
		if _, explicit := cfg.resourceAttr[k]; explicit {
			continue
		}
		attrs = append(attrs, attribute.String(k, v))
	}
	for k, v := range cfg.resourceAttr {
		attrs = append(attrs, attribute.String(k, v))
	}
	return resource.NewSchemaless(attrs...)
}

// applyEnvFallbacks fills in empty config fields from the standard
// OTel env vars before the decision matrix runs. Explicit With...
// options always win — env is the demo-orchestration default.
func applyEnvFallbacks(cfg *setupConfig) {
	if cfg.serviceName == "" {
		cfg.serviceName = os.Getenv("OTEL_SERVICE_NAME")
	}
	if cfg.otlpEndpoint == "" {
		cfg.otlpEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if cfg.otlpEndpoint == "" {
		cfg.otlpEndpoint = DefaultOTLPEndpoint
	}
}

// noopShutdown is the zero-cost ShutdownFunc returned on the Noop
// path. Defined once so the Noop return path doesn't allocate a
// fresh closure per call.
func noopShutdown(context.Context) error { return nil }

// probeOTLPEndpoint TCP-dials the configured endpoint with a short
// timeout to check reachability before constructing the OTLP
// exporter. otlptracegrpc.New is lazy and returns a non-nil exporter
// even when the endpoint is refused, so the dial-failure fallback
// the issue specifies (`a dead stack never breaks make demo`) needs
// an explicit synchronous check here.
func probeOTLPEndpoint(endpoint string) error {
	conn, err := net.DialTimeout("tcp", endpoint, otlpProbeTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// parseResourceAttrEnv parses the OTEL_RESOURCE_ATTRIBUTES env var
// (W3C-style comma-separated key=value pairs) into a map. Malformed
// pairs are silently dropped — invalid env shouldn't crash a demo.
func parseResourceAttrEnv(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(pair[:eq])
		v := strings.TrimSpace(pair[eq+1:])
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out
}
