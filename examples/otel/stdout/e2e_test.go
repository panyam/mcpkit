package main

// End-to-end smoke test for the SEP-414 OTel wire. Boots a real MCP
// server on a httptest URL with the ext/otel adapter wired into
// server.WithTracerProvider, drives it via a real *client.Client with
// an explicit inbound _meta.traceparent, and asserts the in-memory
// span exporter received a tools/call span whose Parent matches the
// inbound traceparent.
//
// Uses an InMemoryExporter rather than the production-path stdouttrace
// exporter so the test can assert on structured ReadOnlySpan values;
// the wire under test is identical otherwise.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	testTraceID  = "4bf92f3577b34da6a3ce929d0e0e4736"
	testParentID = "00f067aa0ba902b7"
	testInbound  = "00-" + testTraceID + "-" + testParentID + "-01"
)

// TestEndToEnd_StdoutDemoWiring exercises the same wiring serve() builds
// (just with an in-memory exporter so the test can read spans back).
// Confirms the SEP-414 wire propagates: the server middleware extracts
// the inbound traceparent, the adapter installs it as the new span's
// remote parent, and the recorded span carries the expected trace ID +
// mcp.method + mcp.tool.name attributes.
func TestEndToEnd_StdoutDemoWiring(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	otelTP := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = otelTP.Shutdown(context.Background()) })

	srv := server.NewServer(
		core.ServerInfo{Name: "otel-stdout-demo", Version: "0.1.0"},
		server.WithTracerProvider(mcpotel.NewProvider(otelTP)),
	)
	registerDemoTools(srv)

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp",
		core.ClientInfo{Name: "otel-stdout-test", Version: "1.0"},
	)
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })

	res, err := c.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"message": "hello"},
		"_meta":     map[string]any{"traceparent": testInbound},
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	spans := exp.GetSpans()
	require.NotEmpty(t, spans, "exporter should have recorded at least one span")

	var toolSpan *tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == "tools/call" {
			toolSpan = &spans[i]
			break
		}
	}
	require.NotNil(t, toolSpan, "no tools/call span exported; saw: %v", spanNames(spans))
	assert.Equal(t, testTraceID, toolSpan.SpanContext.TraceID().String(),
		"tools/call span should inherit the inbound trace ID")
	assert.Equal(t, testParentID, toolSpan.Parent.SpanID().String(),
		"tools/call Parent should match the inbound _meta.traceparent span ID")
	assert.True(t, toolSpan.Parent.IsRemote(),
		"inbound parent must be marked Remote (came from over the wire)")
	attrs := flattenAttrs(toolSpan)
	assert.Equal(t, "tools/call", attrs["mcp.method"])
	assert.Equal(t, "echo", attrs["mcp.tool.name"])
}

// TestSetupTelemetry_Stdout_FromExample is a smoke-test that drives
// the example's own wiring path: commonotel.SetupTelemetry with
// Exporter="stdout" against a writer the test owns. Catches drift
// between the example's flag-driven serve() / runDemo() helpers and
// the underlying helper if the SetupOption surface changes.
func TestSetupTelemetry_Stdout_FromExample(t *testing.T) {
	tmp, err := os.Create(filepath.Join(t.TempDir(), "spans.json"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tmp.Close() })

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(commonotel.ExporterStdout),
		commonotel.WithStdoutWriter(tmp),
		commonotel.WithServiceName(serverServiceName),
	)
	require.NoError(t, err)
	require.NotNil(t, tp)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
}

// TestSetupTelemetry_OTLP_DialFailure_FallsBackToNoop exercises the
// OTLP dial-failure path through the example's helper. Uses a closed
// port (immediately refused) to guarantee the fallback fires
// regardless of whether anything is bound to the default endpoint.
func TestSetupTelemetry_OTLP_DialFailure_FallsBackToNoop(t *testing.T) {
	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(commonotel.ExporterOTLP),
		commonotel.WithOTLPEndpoint("127.0.0.1:1"), // privileged, never bound from user space
		commonotel.WithServiceName(serverServiceName),
	)
	require.NoError(t, err, "dial failure must NOT error — graceful Noop is the contract")
	require.NotNil(t, tp)
	require.NoError(t, shutdown(context.Background()))
}

// TestEndToEnd_BothSidesEmitSpans is the headline check for this PR.
// Each "side" (server and client) wires its own OTel pipeline against a
// separate InMemoryExporter — the same shape the walkthrough builds at
// runtime, just with introspectable exporters instead of stdouttrace.
// The test drives a real tools/call from a real *client.Client WITHOUT
// supplying an explicit _meta.traceparent so the client trace middleware
// stamps its own child traceparent on the wire. Asserts: (1) both
// exporters recorded a tools/call span, (2) they share a TraceID,
// (3) the server span's Parent.SpanID matches the client span's SpanID,
// and (4) instrumentation scopes are correctly differentiated.
//
// This is the "auto-stitch" path that proves the SEP-414 wire works
// end-to-end with no synthetic inbound override — the common case for
// developers wiring observability into a fresh mcpkit stack.
func TestEndToEnd_BothSidesEmitSpans(t *testing.T) {
	serverExp := tracetest.NewInMemoryExporter()
	serverTP := sdktrace.NewTracerProvider(sdktrace.WithSyncer(serverExp))
	t.Cleanup(func() { _ = serverTP.Shutdown(context.Background()) })

	clientExp := tracetest.NewInMemoryExporter()
	clientTP := sdktrace.NewTracerProvider(sdktrace.WithSyncer(clientExp))
	t.Cleanup(func() { _ = clientTP.Shutdown(context.Background()) })

	srv := server.NewServer(
		core.ServerInfo{Name: "otel-stdout-demo", Version: "0.1.0"},
		server.WithTracerProvider(mcpotel.NewProvider(serverTP)),
	)
	registerDemoTools(srv)

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp",
		core.ClientInfo{Name: "otel-stdout-test", Version: "1.0"},
		client.WithTracerProvider(mcpotel.NewProvider(clientTP,
			mcpotel.WithInstrumentationName("github.com/panyam/mcpkit/client"),
		)),
	)
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"message": "hello"},
	})
	require.NoError(t, err)

	clientSpan := findToolsCallSpan(clientExp.GetSpans())
	require.NotNil(t, clientSpan, "client exporter must record a tools/call span; saw %v", spanNames(clientExp.GetSpans()))
	serverSpan := findToolsCallSpan(serverExp.GetSpans())
	require.NotNil(t, serverSpan, "server exporter must record a tools/call span; saw %v", spanNames(serverExp.GetSpans()))

	assert.Equal(t, clientSpan.SpanContext.TraceID(), serverSpan.SpanContext.TraceID(),
		"client and server must share a TraceID — that IS the SEP-414 propagation")
	assert.Equal(t, clientSpan.SpanContext.SpanID(), serverSpan.Parent.SpanID(),
		"server span Parent.SpanID must match the client SpanID — proves the client trace middleware stamped the outbound _meta.traceparent")
	assert.True(t, serverSpan.Parent.IsRemote(),
		"server span Parent must be Remote (came over the wire)")

	clientAttrs := flattenAttrs(clientSpan)
	assert.Equal(t, "tools/call", clientAttrs["mcp.method"])
	assert.Equal(t, "echo", clientAttrs["mcp.tool.name"])

	serverAttrs := flattenAttrs(serverSpan)
	assert.Equal(t, "tools/call", serverAttrs["mcp.method"])
	assert.Equal(t, "echo", serverAttrs["mcp.tool.name"])

	assert.Equal(t, "github.com/panyam/mcpkit/client", clientSpan.InstrumentationScope.Name,
		"client-side spans should be tagged with the client instrumentation library name")
	assert.Equal(t, "github.com/panyam/mcpkit/server", serverSpan.InstrumentationScope.Name,
		"server-side spans should keep the default instrumentation library name")
}

// Reference json.Unmarshal so the encoding/json import remains used even
// if other helpers are removed; assertion bodies above operate on
// structured exporter output rather than raw JSON. Lint guard only.
var _ = json.Unmarshal

func findToolsCallSpan(spans []tracetest.SpanStub) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == "tools/call" {
			return &spans[i]
		}
	}
	return nil
}

func spanNames(spans []tracetest.SpanStub) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Name)
	}
	return out
}

func flattenAttrs(s *tracetest.SpanStub) map[string]string {
	out := make(map[string]string, len(s.Attributes))
	for _, kv := range s.Attributes {
		out[string(kv.Key)] = strings.TrimSpace(kv.Value.AsString())
	}
	return out
}

// TestEndToEnd_ToolMenu_AllToolsEmit drives each of the four
// registerDemoTools tools and asserts the tool-specific span shape an
// operator would see in Tempo. This is the regression guard for the
// "Explore trace shapes" looping step in the walkthrough — if any
// tool's span attribute pattern drifts, the demo's teaching value
// drifts with it.
func TestEndToEnd_ToolMenu_AllToolsEmit(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	otelTP := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = otelTP.Shutdown(context.Background()) })

	srv := server.NewServer(
		core.ServerInfo{Name: "otel-stdout-demo", Version: "0.1.0"},
		server.WithTracerProvider(mcpotel.NewProvider(otelTP)),
	)
	registerDemoTools(srv)

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp",
		core.ClientInfo{Name: "otel-stdout-test", Version: "1.0"},
	)
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })

	type expectation struct {
		assertSpan func(t *testing.T, span *tracetest.SpanStub)
	}
	cases := map[string]expectation{
		"echo": {
			assertSpan: func(t *testing.T, span *tracetest.SpanStub) {
				attrs := flattenAttrs(span)
				assert.Equal(t, "echo", attrs["mcp.tool.name"])
				assert.NotEqual(t, "true", attrs["mcp.tool.is_error"],
					"echo is the baseline; must not be flagged as a tool error")
			},
		},
		"slow_echo": {
			assertSpan: func(t *testing.T, span *tracetest.SpanStub) {
				attrs := flattenAttrs(span)
				assert.Equal(t, "slow_echo", attrs["mcp.tool.name"])
				duration := span.EndTime.Sub(span.StartTime)
				assert.GreaterOrEqual(t, duration, 700*time.Millisecond,
					"slow_echo should have ~750ms duration; got %s", duration)
			},
		},
		"failing_tool": {
			assertSpan: func(t *testing.T, span *tracetest.SpanStub) {
				attrs := flattenAttrs(span)
				assert.Equal(t, "failing_tool", attrs["mcp.tool.name"])
				assert.Equal(t, "true", attrs["mcp.tool.is_error"],
					"failing_tool's IsError result must surface as mcp.tool.is_error=true on the span")
			},
		},
		"count_tool": {
			assertSpan: func(t *testing.T, span *tracetest.SpanStub) {
				attrs := flattenAttrs(span)
				assert.Equal(t, "count_tool", attrs["mcp.tool.name"])
			},
		},
	}

	for _, toolName := range []string{"echo", "slow_echo", "failing_tool", "count_tool"} {
		t.Run(toolName, func(t *testing.T) {
			exp.Reset()

			_, err := c.Call("tools/call", map[string]any{
				"name":      toolName,
				"arguments": map[string]any{"message": "test"},
			})
			require.NoError(t, err)

			spans := exp.GetSpans()
			toolSpan := findToolsCallSpan(spans)
			require.NotNil(t, toolSpan, "tools/call span must be exported for %s; saw %v", toolName, spanNames(spans))
			cases[toolName].assertSpan(t, toolSpan)
		})
	}

	// count_tool's progress notifications cross the wire as
	// `notifications/progress` messages, but those aren't dispatch-
	// path spans on the server (notifications don't have request IDs
	// and the trace middleware only spans request dispatches). The
	// per-notification _meta.traceparent injection is verified by
	// server/trace_middleware_test.go::TestTraceMiddleware_InjectsOutboundNotification_Meta
	// — covered there, not duplicated here.
}

// Ensure the `time` package import stays meaningful for the test
// file's slow_echo duration assertion.
var _ = time.Millisecond
