package otel_test

// SEP-414 P6 MRTR multi-round trace stitching (issue 682) — end-to-end
// against the real OTel SDK. Drives a full CallToolWithInputs round trip
// against a server tracer + client tracer (both real Providers), then
// asserts the recorded round-2 server span carries an OTel Link whose
// TraceID matches round-1's server span — the wire-shape correctness
// proof that the mcpkit-internal fakes can't deliver.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMRTRTracelink_E2E_RoundTwoLinksToRoundOne(t *testing.T) {
	serverExp := tracetest.NewInMemoryExporter()
	serverSDK := sdktrace.NewTracerProvider(sdktrace.WithSyncer(serverExp))
	defer serverSDK.Shutdown(context.Background())
	serverTP := mcpotel.NewProvider(serverSDK)

	clientExp := tracetest.NewInMemoryExporter()
	clientSDK := sdktrace.NewTracerProvider(sdktrace.WithSyncer(clientExp))
	defer clientSDK.Shutdown(context.Background())
	clientTP := mcpotel.NewProvider(clientSDK)

	srv := server.NewServer(
		core.ServerInfo{Name: "mrtr-e2e-trace", Version: "0.0.1"},
		server.WithTracerProvider(serverTP),
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "greet",
			Description: "asks for the user's name then greets them",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
					},
				})
			}
			raw := ctx.InputResponse("user_name")
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Name string `json:"name"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &er); err != nil {
				return core.ErrorResult("malformed"), nil
			}
			return core.TextResult("Hello, " + er.Content.Name + "!"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "mrtr-e2e-client", Version: "0.0.1"},
		client.WithTracerProvider(clientTP),
		client.WithElicitationHandler(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
			return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": "Alice"}}, nil
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	res, err := client.CallToolWithInputs(context.Background(), c,
		"greet", map[string]any{},
		client.DefaultInputHandler(c),
	)
	require.NoError(t, err)
	require.NotNil(t, res.Sync)
	assert.Contains(t, res.Sync.Content[0].Text, "Alice")

	// Server-side spans for tools/call. There may be other spans for
	// initialize / notifications/initialized etc; filter to the two
	// tools/call dispatch spans.
	serverSpans := serverExp.GetSpans()
	var round1, round2 sdktrace.ReadOnlySpan
	for _, s := range serverSpans {
		if s.Name != "tools/call" {
			continue
		}
		if round1 == nil {
			round1 = s.Snapshot()
			continue
		}
		round2 = s.Snapshot()
		break
	}
	require.NotNil(t, round1, "round-1 server tools/call span MUST exist")
	require.NotNil(t, round2, "round-2 server tools/call span MUST exist")

	// Round-2 server span MUST carry an OTel Link whose TraceID matches
	// round-1's TraceID. This is the wire-shape correctness proof —
	// without the client-side tracelink injection AND the server-side
	// AddLink, this assertion fails.
	links := round2.Links()
	require.NotEmpty(t, links, "round-2 server span MUST carry an OTel Link back to round-1")

	round1TraceID := round1.SpanContext().TraceID()
	var matched bool
	for _, lk := range links {
		if lk.SpanContext.TraceID() == round1TraceID {
			matched = true
			break
		}
	}
	assert.True(t, matched,
		"round-2's Link list MUST contain an entry whose TraceID == round-1's TraceID; got links=%v, want match for %s",
		links, round1TraceID)
}
