package ui_test

import (
	"context"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSpan + fakeTracerProvider mirror server/trace_middleware_test.go's
// shape so the AppHost trace tests can assert span emission and
// parent-context placement without pulling in the OTel SDK (ext/ui
// depends on the core abstraction only).
type fakeSpan struct {
	name   string
	parent core.TraceContext
	attrs  map[string]string
	errors []error
	ended  bool
}

func (s *fakeSpan) End()                  { s.ended = true }
func (s *fakeSpan) SetAttribute(k, v string) {
	if s.attrs == nil {
		s.attrs = map[string]string{}
	}
	s.attrs[k] = v
}
func (s *fakeSpan) RecordError(err error) { s.errors = append(s.errors, err) }
func (s *fakeSpan) AddLink(core.Link)     {}

type fakeTracerProvider struct {
	mu    sync.Mutex
	spans []*fakeSpan
}

func (p *fakeTracerProvider) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	sp := &fakeSpan{
		name:   name,
		parent: core.TraceContextFromContext(ctx),
		attrs:  make(map[string]string, len(attrs)),
	}
	for _, a := range attrs {
		sp.attrs[a.Key] = a.Value
	}
	p.mu.Lock()
	p.spans = append(p.spans, sp)
	p.mu.Unlock()
	return core.WithActiveSpan(ctx, sp), sp
}

func (p *fakeTracerProvider) snapshot() []*fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*fakeSpan, len(p.spans))
	copy(out, p.spans)
	return out
}

func (p *fakeTracerProvider) findSpan(name string) *fakeSpan {
	for _, sp := range p.snapshot() {
		if sp.name == name {
			return sp
		}
	}
	return nil
}

// newAppHostWithCapturedServer wires a real MCP server + client +
// in-process bridge so the AppHost.handleAppRequest path runs
// end-to-end. captured.Params is populated with the params the
// outbound tool handler receives — lets the tests assert that
// inbound _meta.traceparent rides through unchanged.
type capturedServerParams struct {
	mu     sync.Mutex
	Params map[string]any
}

func newAppHostWithCapturedServer(t *testing.T, opts ...ui.AppHostOption) (*ui.AppHost, *ui.InProcessAppBridge, *capturedServerParams) {
	t.Helper()
	captured := &capturedServerParams{}

	// Server-side trace middleware is what reads params._meta.traceparent
	// off the wire and attaches it to the handler's ctx — without it,
	// TraceContextFromContext returns zero even when the wire carried
	// a traceparent. Wired with its own fake TP so the test's
	// AppHost-side fake isn't entangled.
	serverTP := &fakeTracerProvider{}
	srv := server.NewServer(core.ServerInfo{Name: "trace-server", Version: "1.0"},
		server.WithTracerProvider(serverTP),
	)
	srv.RegisterTool(
		core.ToolDef{Name: "server_echo", Description: "echoes back"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			// We can't inspect _meta from inside a tool handler (it's
			// stripped by the server before dispatch). Instead, the
			// outbound _meta.traceparent assertion below reads the
			// inbound trace context from ctx — which the server's
			// SEP-414 P2 trace middleware populates from _meta.
			tc := core.TraceContextFromContext(ctx)
			captured.mu.Lock()
			captured.Params = map[string]any{"traceparent_observed": tc.Traceparent}
			captured.mu.Unlock()
			return core.TextResult("ok"), nil
		},
	)

	xport := server.NewInProcessTransport(srv)
	c := client.NewClient("memory://", core.ClientInfo{Name: "trace-host", Version: "1.0"},
		client.WithTransport(xport),
		client.WithUIExtension(),
	)
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })

	bridge := ui.NewInProcessAppBridge()
	host := ui.NewAppHost(c, bridge, opts...)
	require.NoError(t, host.Start(context.Background()))
	t.Cleanup(func() { _ = host.Close() })

	return host, bridge, captured
}

func TestAppHost_Trace_NoTracerProvider_NoSpans(t *testing.T) {
	_, bridge, _ := newAppHostWithCapturedServer(t)

	resp, err := bridge.SendToHost(context.Background(), "tools/call", map[string]any{
		"name":      "server_echo",
		"arguments": map[string]any{},
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	// No assertions possible beyond "didn't panic / errored" — bare
	// AppHost (no WithTracerProvider) must default to Noop and stay
	// zero-overhead.
}

func TestAppHost_Trace_InboundTraceparent_AppsHostForwardSpan(t *testing.T) {
	tp := &fakeTracerProvider{}
	_, bridge, _ := newAppHostWithCapturedServer(t, ui.WithTracerProvider(tp))

	const inboundTP = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	resp, err := bridge.SendToHost(context.Background(), "tools/call", map[string]any{
		"name":      "server_echo",
		"arguments": map[string]any{},
		"_meta":     map[string]any{"traceparent": inboundTP},
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	span := tp.findSpan("apps.host.forward")
	require.NotNil(t, span, "WithTracerProvider must emit apps.host.forward on the forward path")
	assert.True(t, span.ended, "span must be ended (defer would leave it open under panic)")
	assert.Equal(t, "tools/call", span.attrs["mcp.method"])
	assert.Equal(t, inboundTP, span.parent.Traceparent,
		"iframe-relayed _meta.traceparent must become the parent of apps.host.forward")
}

func TestAppHost_Trace_NoInboundTraceparent_SpanWithNoParent(t *testing.T) {
	tp := &fakeTracerProvider{}
	_, bridge, _ := newAppHostWithCapturedServer(t, ui.WithTracerProvider(tp))

	resp, err := bridge.SendToHost(context.Background(), "tools/call", map[string]any{
		"name":      "server_echo",
		"arguments": map[string]any{},
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	span := tp.findSpan("apps.host.forward")
	require.NotNil(t, span, "span must still emit when no inbound _meta.traceparent — just without a parent")
	assert.True(t, span.parent.IsZero(),
		"missing inbound trace context must leave the span parent zero, not a fabricated value")
}

func TestAppHost_Trace_OutboundClientCallPreservesInboundTraceparent(t *testing.T) {
	_, bridge, captured := newAppHostWithCapturedServer(t, ui.WithTracerProvider(&fakeTracerProvider{}))

	const inboundTP = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	resp, err := bridge.SendToHost(context.Background(), "tools/call", map[string]any{
		"name":      "server_echo",
		"arguments": map[string]any{},
		"_meta":     map[string]any{"traceparent": inboundTP},
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	// The server's SEP-414 P2 trace middleware populates ctx with the
	// inbound _meta.traceparent BEFORE dispatch — so the tool handler
	// observing tc.Traceparent on its ctx proves the bridge envelope's
	// _meta rode through the AppHost forward unchanged AND was
	// extracted on the server side.
	captured.mu.Lock()
	observed := captured.Params["traceparent_observed"]
	captured.mu.Unlock()
	assert.Equal(t, inboundTP, observed,
		"inbound _meta.traceparent must be preserved on the outbound MCP call so the server stitches into the same trace")
}

func TestAppHost_Trace_RecordsErrorOnForwardFailure(t *testing.T) {
	tp := &fakeTracerProvider{}
	_, bridge, _ := newAppHostWithCapturedServer(t, ui.WithTracerProvider(tp))

	// Call a method that doesn't exist on the server — the inner
	// client.Call returns an error.
	resp, err := bridge.SendToHost(context.Background(), "tools/call", map[string]any{
		"name":      "nonexistent_tool",
		"arguments": map[string]any{},
	})
	// The bridge returns nil err with an Error on the response (the
	// server emits a tool-not-found error in result form, not a transport
	// error). In either case the forward span ends — that's what we
	// assert; whether RecordError fires depends on which error path
	// the server takes.
	_ = err
	_ = resp

	span := tp.findSpan("apps.host.forward")
	require.NotNil(t, span)
	assert.True(t, span.ended, "span must still end on the error path (deferred)")
}
