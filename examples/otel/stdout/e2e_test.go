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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
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
	registerEcho(srv)

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

// TestNewOTelPipeline_HappyPath isolates the pipeline helper from the
// rest of serve(). Used as a regression guard against accidental
// breakage in newOTelPipeline's TracerProvider construction (e.g., the
// helper drifting away from the SDK's current constructor signature).
func TestNewOTelPipeline_HappyPath(t *testing.T) {
	// Pipe stdout-substitute through a temp file the test owns so the
	// pipeline's writer doesn't pollute the test runner's terminal.
	tmp, err := os.Create(filepath.Join(t.TempDir(), "spans.json"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tmp.Close() })

	tp, shutdown, err := newOTelPipeline(tmp)
	require.NoError(t, err)
	require.NotNil(t, tp)
	require.NotNil(t, shutdown)

	// Shutdown is a closure over the SDK's Shutdown; calling it twice
	// should be safe (the SDK guards internally). The test asserts no
	// panic.
	shutdown()
	shutdown()
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
