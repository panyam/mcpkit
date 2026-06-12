package server

// SEP-414 Phase 2 — server-side OTel span emission + W3C trace context
// propagation tests. Uses a fake TracerProvider that records every span
// it starts so the suite can run without depending on go.opentelemetry.io/otel.
//
// White-box (`package server`) because the test wires the dispatcher
// directly via dispatchWithNotifyAndRequest to attach test notify/request
// hooks without spinning up a transport. The OTel SDK adapter and any
// end-to-end exporter coverage land in P4 with the new ext/otel/ module.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Valid W3C version-00 traceparent values used across the suite.
	tpInbound = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	tpHTTPHdr = "00-aaaa1234567890abcdef1234567890aa-1111222233334444-01"
	tpChild   = "00-0af7651916cd43dd8448eb211c80319c-cccccccccccccccc-01"
)

// fakeSpan records every attribute / RecordError call and end-state so
// individual tests can assert the exact OTel signal emitted.
type fakeSpan struct {
	mu     sync.Mutex
	name   string
	attrs  map[string]string
	errs   []error
	ended  bool
	parent core.TraceContext // captured from ctx at StartSpan
	links  []core.Link        // recorded via AddLink — tests assert MRTR tracelink stitching
}

func (s *fakeSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *fakeSpan) SetAttribute(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = make(map[string]string)
	}
	s.attrs[k] = v
}

func (s *fakeSpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *fakeSpan) AddLink(l core.Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links = append(s.links, l)
}

func (s *fakeSpan) linkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.links)
}

func (s *fakeSpan) linkAt(i int) core.Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.links[i]
}

func (s *fakeSpan) attr(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attrs[k]
}

// fakeTracerProvider records every span StartSpan creates. When childTC
// is non-zero, StartSpan attaches it via core.WithTraceContext to mimic
// an OTel adapter that updates the active TraceContext with the new
// child span's identity — drives the outbound-_meta assertions below.
type fakeTracerProvider struct {
	mu      sync.Mutex
	spans   []*fakeSpan
	childTC core.TraceContext
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
	if !p.childTC.IsZero() {
		ctx = core.WithTraceContext(ctx, p.childTC)
	}
	return ctx, sp
}

func (p *fakeTracerProvider) snapshot() []*fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*fakeSpan, len(p.spans))
	copy(out, p.spans)
	return out
}

// newInitializedServer returns a server with the dispatcher flipped to
// initialized so tests can call tools/call without the full handshake.
func newInitializedServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	srv := NewServer(core.ServerInfo{Name: "trace-test", Version: "1.0"}, opts...)
	srv.dispatcher.initialized = true
	return srv
}

// dispatchToolsCall is a one-line wrapper that builds a tools/call request
// envelope and routes it through dispatchWithNotifyAndRequest so tests can
// supply their own notify and request hooks.
func dispatchToolsCall(t *testing.T, srv *Server, ctx context.Context, params string, notify core.NotifyFunc, request core.RequestFunc) (*core.Response, error) {
	t.Helper()
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(params),
	}
	return srv.dispatchWithNotifyAndRequest(srv.dispatcher, ctx, nil, notify, request, req)
}

// TestTraceMiddleware_NoProviderInstallsNothing verifies that the default
// configuration (no WithTracerProvider) does not install the trace
// middleware, does not parse _meta for trace fields, and does not inject
// _meta on outbound notifications. This is the zero-overhead path.
func TestTraceMiddleware_NoProviderInstallsNothing(t *testing.T) {
	srv := newInitializedServer(t)
	srv.RegisterTool(core.ToolDef{Name: "noop"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		ctx.Notify("notifications/test", map[string]any{"hello": "world"})
		return core.TextResult("ok"), nil
	})

	var captured []map[string]any
	notify := core.NotifyFunc(func(method string, params any) {
		raw, _ := json.Marshal(params)
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		captured = append(captured, obj)
	})

	params := `{"name":"noop","_meta":{"traceparent":"` + tpInbound + `"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, notify, nil)
	require.NoError(t, err)

	require.Len(t, captured, 1)
	_, hasTraceparent := captured[0]["_meta"].(map[string]any)
	if hasTraceparent {
		m := captured[0]["_meta"].(map[string]any)
		assert.NotContains(t, m, "traceparent", "outbound notify must not carry traceparent when no provider is configured")
	}
}

// TestTraceMiddleware_NoopProviderInstallsNothing pins the behavior that
// passing core.NoopTracerProvider{} explicitly is equivalent to the
// default — no middleware installed, no _meta injection.
func TestTraceMiddleware_NoopProviderInstallsNothing(t *testing.T) {
	srv := newInitializedServer(t, WithTracerProvider(core.NoopTracerProvider{}))
	srv.RegisterTool(core.ToolDef{Name: "noop"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		ctx.Notify("notifications/test", nil)
		return core.TextResult("ok"), nil
	})

	var notifyCalls int
	notify := core.NotifyFunc(func(method string, params any) {
		notifyCalls++
		if params != nil {
			t.Fatalf("Noop provider must not inject _meta; got params=%v", params)
		}
	})

	params := `{"name":"noop","_meta":{"traceparent":"` + tpInbound + `"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, notify, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, notifyCalls)
}

// TestTraceMiddleware_ExtractsInboundMetaTraceparent verifies that the
// middleware reads _meta.traceparent from the request envelope and uses
// it as the parent of the emitted span.
func TestTraceMiddleware_ExtractsInboundMetaTraceparent(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		assert.Equal(t, tpInbound, ctx.TraceContext().Traceparent, "handler must observe extracted trace context via ctx.TraceContext()")
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo","_meta":{"traceparent":"` + tpInbound + `","tracestate":"vendor=on"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, "tools/call", spans[0].name)
	assert.Equal(t, tpInbound, spans[0].parent.Traceparent)
	assert.Equal(t, "vendor=on", spans[0].parent.Tracestate)
	assert.True(t, spans[0].ended, "span must be ended exactly once on return")
}

// TestTraceMiddleware_FallsBackToContextTraceContext verifies that when
// no _meta.traceparent is in the request, the middleware reads the
// TraceContext that the transport (or another middleware) attached via
// core.WithTraceContext — the same path the streamable HTTP transport
// uses for the SEP-2028 header bridge.
func TestTraceMiddleware_FallsBackToContextTraceContext(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{Traceparent: tpHTTPHdr})
	params := `{"name":"echo"}`
	_, err := dispatchToolsCall(t, srv, ctx, params, nil, nil)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, tpHTTPHdr, spans[0].parent.Traceparent)
}

// TestTraceMiddleware_MetaWinsOverContext verifies precedence: an in-band
// _meta.traceparent always beats whatever the transport bridged onto ctx.
// Mirrors SEP-2028's spec stance — the HTTP header is a convenience, but
// the request body is authoritative when both are present.
func TestTraceMiddleware_MetaWinsOverContext(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{Traceparent: tpHTTPHdr})
	params := `{"name":"echo","_meta":{"traceparent":"` + tpInbound + `"}}`
	_, err := dispatchToolsCall(t, srv, ctx, params, nil, nil)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, tpInbound, spans[0].parent.Traceparent, "_meta.traceparent must win over ctx-attached value")
}

// TestTraceMiddleware_MalformedTraceparentDropsSilently verifies that an
// inbound traceparent failing W3C validation does NOT prevent the span
// from emitting — the span just has no parent. Matches the contract in
// core.ExtractTraceContext, which returns a zero TraceContext for
// malformed input and never errors.
func TestTraceMiddleware_MalformedTraceparentDropsSilently(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		assert.True(t, ctx.TraceContext().IsZero(), "handler must observe zero TraceContext when inbound traceparent is malformed")
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo","_meta":{"traceparent":"not-a-real-traceparent","tracestate":"vendor=x"}}`
	resp, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.True(t, spans[0].parent.IsZero())
}

// TestTraceMiddleware_SetsCoreAttributes verifies the inbound-span
// attribute set: mcp.method always, mcp.tool.name on tools/call.
// mcp.session.id is exercised by the streamable HTTP integration test
// since SetSessionID needs a session context.
func TestTraceMiddleware_SetsCoreAttributes(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo"}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, "tools/call", spans[0].attr("mcp.method"))
	assert.Equal(t, "echo", spans[0].attr("mcp.tool.name"))
}

// dispatchResourcesRead builds a resources/read request envelope and
// routes it through dispatchWithNotifyAndRequest. Sibling to
// dispatchToolsCall, used by the SEP-414 P7 (#748) tests below.
func dispatchResourcesRead(t *testing.T, srv *Server, ctx context.Context, params string) (*core.Response, error) {
	t.Helper()
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "resources/read",
		Params: json.RawMessage(params),
	}
	return srv.dispatchWithNotifyAndRequest(srv.dispatcher, ctx, nil, nil, nil, req)
}

// TestTraceMiddleware_EmitsSkillAttrsOnManifestRead is the SEP-414 P7
// happy path: a resources/read targeting `skill://acme/billing/refunds/
// SKILL.md` emits mcp.resource.uri, mcp.skill.uri, mcp.skill.path, and
// mcp.skill.file so server-side dashboards can chart fetch volume per
// skill. Dispatch returns an "unknown resource" error because the
// resource isn't registered, but the span attrs were stamped before the
// inner handler ran.
func TestTraceMiddleware_EmitsSkillAttrsOnManifestRead(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))

	const uri = "skill://acme/billing/refunds/SKILL.md"
	params := `{"uri":"` + uri + `"}`
	_, err := dispatchResourcesRead(t, srv, context.Background(), params)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, "resources/read", spans[0].attr("mcp.method"))
	assert.Equal(t, uri, spans[0].attr("mcp.resource.uri"))
	assert.Equal(t, uri, spans[0].attr("mcp.skill.uri"))
	assert.Equal(t, "acme/billing/refunds", spans[0].attr("mcp.skill.path"))
	assert.Equal(t, "SKILL.md", spans[0].attr("mcp.skill.file"))
}

// TestTraceMiddleware_EmitsResourceURI_NonSkillRead pins the contract
// boundary: mcp.resource.uri is emitted for ANY resources/read, but the
// skill-specific attrs (mcp.skill.*) stay absent when the URI does not
// use the skill:// scheme.
func TestTraceMiddleware_EmitsResourceURI_NonSkillRead(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))

	const uri = "file:///etc/motd"
	params := `{"uri":"` + uri + `"}`
	_, err := dispatchResourcesRead(t, srv, context.Background(), params)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, uri, spans[0].attr("mcp.resource.uri"))
	assert.Empty(t, spans[0].attr("mcp.skill.uri"))
	assert.Empty(t, spans[0].attr("mcp.skill.path"))
	assert.Empty(t, spans[0].attr("mcp.skill.file"))
}

// TestTraceMiddleware_NonManifestSkillURI_EmitsURIOnly verifies that a
// non-manifest skill URI (e.g., a supporting file like references/
// FORMS.md) gets mcp.skill.uri but NOT mcp.skill.path / mcp.skill.file —
// SEP-2640 documents that the skill/file boundary cannot be recovered
// from the URI alone for non-manifest reads. Span attrs follow the
// strict parser's behavior.
func TestTraceMiddleware_NonManifestSkillURI_EmitsURIOnly(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))

	const uri = "skill://pdf-processing/references/FORMS.md"
	params := `{"uri":"` + uri + `"}`
	_, err := dispatchResourcesRead(t, srv, context.Background(), params)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, uri, spans[0].attr("mcp.resource.uri"))
	assert.Equal(t, uri, spans[0].attr("mcp.skill.uri"))
	assert.Empty(t, spans[0].attr("mcp.skill.path"))
	assert.Empty(t, spans[0].attr("mcp.skill.file"))
}

// TestTraceMiddleware_RPCError_RecordsErrorAndCode verifies that a
// JSON-RPC error response sets mcp.error.code and calls RecordError with
// the error message. Routes through the tools/call "unknown tool" path
// so the response carries a real *core.Error from dispatch.
func TestTraceMiddleware_RPCError_RecordsErrorAndCode(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))

	params := `{"name":"does-not-exist"}`
	resp, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.NotEmpty(t, spans[0].attr("mcp.error.code"))
	require.Len(t, spans[0].errs, 1)
	assert.Contains(t, spans[0].errs[0].Error(), "unknown tool")
}

// TestTraceMiddleware_ToolIsError_SetsAttribute verifies that a
// tools/call returning a ToolResult with IsError stamps
// mcp.tool.is_error="true" on the span. The JSON-RPC response itself is
// success (resp.Error == nil) — this attribute is the only signal that
// distinguishes "tool ran and reported failure" from "tool ran and
// reported success."
func TestTraceMiddleware_ToolIsError_SetsAttribute(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "bad"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.ToolResult{IsError: true, Content: []core.Content{{Type: "text", Text: "boom"}}}, nil
	})

	params := `{"name":"bad"}`
	resp, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, "true", spans[0].attr("mcp.tool.is_error"))
}

// TestTraceMiddleware_InjectsOutboundNotification_Meta verifies that when
// the handler emits a notification (ctx.Notify, EmitProgress, EmitLog,
// EmitContent — all funnel through sc.notify), the params carry
// _meta.traceparent set to the active span's TraceContext. Uses the
// fakeTracerProvider.childTC path so the assertion proves the wrap reads
// the post-StartSpan trace context (not just the inbound parent).
func TestTraceMiddleware_InjectsOutboundNotification_Meta(t *testing.T) {
	tp := &fakeTracerProvider{
		childTC: core.TraceContext{Traceparent: tpChild, Tracestate: "vendor=child"},
	}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "emit"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		ctx.Notify("notifications/test", map[string]any{"hello": "world"})
		return core.TextResult("ok"), nil
	})

	var captured map[string]any
	notify := core.NotifyFunc(func(method string, params any) {
		raw, _ := json.Marshal(params)
		_ = json.Unmarshal(raw, &captured)
	})

	params := `{"name":"emit","_meta":{"traceparent":"` + tpInbound + `"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, notify, nil)
	require.NoError(t, err)

	meta, _ := captured["_meta"].(map[string]any)
	require.NotNil(t, meta, "outbound notify params must include _meta")
	assert.Equal(t, tpChild, meta[core.MetaKeyTraceparent])
	assert.Equal(t, "vendor=child", meta[core.MetaKeyTracestate])
	assert.Equal(t, "world", captured["hello"], "non-_meta fields must pass through unchanged")
}

// TestTraceMiddleware_InjectsOutboundRequest_Meta verifies the same
// injection for server-to-client requests (sampling/createMessage,
// elicitation/create, roots/list — all routed through sc.request).
func TestTraceMiddleware_InjectsOutboundRequest_Meta(t *testing.T) {
	tp := &fakeTracerProvider{
		childTC: core.TraceContext{Traceparent: tpChild},
	}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "ask"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		_, _ = ctx.Sample(core.CreateMessageRequest{MaxTokens: 16})
		return core.TextResult("ok"), nil
	})

	// Force the legacy push path to be available so ctx.Sample reaches
	// the test request hook (handler_context.Sample early-returns when
	// sc.request == nil or client did not declare sampling capability).
	srv.dispatcher.clientCaps = core.ClientCapabilities{Sampling: &struct{}{}}

	var captured map[string]any
	request := core.RequestFunc(func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		_ = json.Unmarshal(raw, &captured)
		return json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"ok"},"model":"x"}`), nil
	})

	params := `{"name":"ask","_meta":{"traceparent":"` + tpInbound + `"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, request)
	require.NoError(t, err)

	meta, _ := captured["_meta"].(map[string]any)
	require.NotNil(t, meta, "outbound server-to-client request must carry _meta")
	assert.Equal(t, tpChild, meta[core.MetaKeyTraceparent])
}

// TestTraceMiddleware_UserMiddlewareRunsInsideSpan verifies that
// WithTracerProvider installs the trace middleware as the OUTERMOST
// layer — user middleware (rate limit, audit, custom auth) executes
// inside the span, so its latency is captured.
func TestTraceMiddleware_UserMiddlewareRunsInsideSpan(t *testing.T) {
	tp := &fakeTracerProvider{}
	var order []string
	userMW := func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		order = append(order, "user-before")
		resp, err := next(ctx, req)
		order = append(order, "user-after")
		return resp, err
	}

	srv := newInitializedServer(t,
		WithTracerProvider(tp),
		WithMiddleware(userMW),
	)
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		order = append(order, "handler")
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo"}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	require.Equal(t, []string{"user-before", "handler", "user-after"}, order)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.True(t, spans[0].ended)
}

// Helper-level tests for InjectTraceContextIntoParams moved to
// core/trace_test.go alongside the promoted symbol — see
// TestInjectTraceContextIntoParams_RespectsExplicitMeta and
// TestInjectTraceContextIntoParams_HandlesNilAndNonObject there.

// TestWithTraceContextFromHTTPHeaders_Bridges directly exercises the
// SEP-2028 helper. Covers the three branches: absent header → ctx
// unchanged; valid header → ctx carries the TraceContext; malformed
// header → ctx unchanged (W3C MUST NOT forward).
func TestWithTraceContextFromHTTPHeaders_Bridges(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		ctx := withTraceContextFromHTTPHeaders(context.Background(), http.Header{})
		assert.True(t, core.TraceContextFromContext(ctx).IsZero())
	})
	t.Run("valid", func(t *testing.T) {
		h := http.Header{}
		h.Set(httpHeaderTraceparent, tpHTTPHdr)
		h.Set(httpHeaderTracestate, "vendor=on")
		ctx := withTraceContextFromHTTPHeaders(context.Background(), h)
		got := core.TraceContextFromContext(ctx)
		assert.Equal(t, tpHTTPHdr, got.Traceparent)
		assert.Equal(t, "vendor=on", got.Tracestate)
	})
	t.Run("malformed", func(t *testing.T) {
		h := http.Header{}
		h.Set(httpHeaderTraceparent, "not-a-traceparent")
		h.Set(httpHeaderTracestate, "vendor=on")
		ctx := withTraceContextFromHTTPHeaders(context.Background(), h)
		got := core.TraceContextFromContext(ctx)
		assert.True(t, got.IsZero(), "malformed traceparent must drop tracestate too (W3C)")
	})
}

// TestParseToolCallName_Robust exercises the attribute helper's failure
// modes: empty params, non-JSON, missing name field.
func TestParseToolCallName_Robust(t *testing.T) {
	assert.Equal(t, "", parseToolCallName(nil))
	assert.Equal(t, "", parseToolCallName(json.RawMessage(`not-json`)))
	assert.Equal(t, "", parseToolCallName(json.RawMessage(`{"other":"x"}`)))
	assert.Equal(t, "echo", parseToolCallName(json.RawMessage(`{"name":"echo","arguments":{}}`)))
}

// TestIsToolErrorResult covers the three result-shape branches: typed
// value, typed pointer, and the json round-trip fallback for arbitrary
// JSON-marshalable shapes (e.g., a map carrying isError).
func TestIsToolErrorResult(t *testing.T) {
	assert.False(t, isToolErrorResult(nil))
	assert.False(t, isToolErrorResult(core.ToolResult{IsError: false}))
	assert.True(t, isToolErrorResult(core.ToolResult{IsError: true}))
	assert.True(t, isToolErrorResult(&core.ToolResult{IsError: true}))
	assert.True(t, isToolErrorResult(map[string]any{"isError": true}))
	assert.False(t, isToolErrorResult(map[string]any{"isError": false}))
	// Channels do not marshal — fallback returns false rather than panicking.
	ch := make(chan int)
	assert.False(t, isToolErrorResult(ch))
}

// TestStreamableTransport_BridgesHTTPTraceparentHeader is the integration
// check for P2.3: a real HTTP POST carrying the `traceparent` header (no
// in-band _meta) reaches the trace middleware as the inbound trace
// context. Exercises the handlePost bridge call.
func TestStreamableTransport_BridgesHTTPTraceparentHeader(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := NewServer(core.ServerInfo{Name: "trace-http", Version: "1.0"},
		WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	handler := srv.Handler(WithStreamableHTTP(true), WithStateless(true))

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`)
	r, err := http.NewRequest(http.MethodPost, "/mcp", body)
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set(httpHeaderTraceparent, tpHTTPHdr)

	w := newRespRecorder()
	handler.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.statusCode())

	spans := tp.snapshot()
	require.NotEmpty(t, spans)
	// The tools/call span (last in the chain — initialize, if any, would
	// also have been recorded, but stateless mode skips it).
	last := spans[len(spans)-1]
	assert.Equal(t, tpHTTPHdr, last.parent.Traceparent, "HTTP traceparent header must bridge into the inbound span's parent")
}

// respRecorder is a minimal http.ResponseWriter that captures the status
// code and body so the integration test does not depend on net/http/httptest.
type respRecorder struct {
	mu      sync.Mutex
	hdr     http.Header
	status  int
	written []byte
}

func newRespRecorder() *respRecorder {
	return &respRecorder{hdr: http.Header{}}
}

func (r *respRecorder) Header() http.Header        { return r.hdr }
func (r *respRecorder) WriteHeader(statusCode int) { r.mu.Lock(); r.status = statusCode; r.mu.Unlock() }
func (r *respRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.written = append(r.written, b...)
	r.mu.Unlock()
	return len(b), nil
}

func (r *respRecorder) statusCode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// --- SEP-414 P6 MRTR tracelink (issue 682) ---

const tpMRTRLink = "00-deadbeefcafebabe0123456789abcdef-fedcba9876543210-01"

// TestTraceMiddleware_ReadsTracelink_AddsLinkToDispatchSpan pins the
// server-side half of MRTR trace stitching: when an inbound tools/call
// carries `_meta.io.modelcontextprotocol/tracelink`, the dispatch span
// receives an AddLink call carrying that TraceContext. The link makes
// round-2+ server spans navigable back to round-1 in Tempo / Grafana.
func TestTraceMiddleware_ReadsTracelink_AddsLinkToDispatchSpan(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo","_meta":{"traceparent":"` + tpInbound + `","io.modelcontextprotocol/tracelink":"` + tpMRTRLink + `"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	require.Equal(t, 1, spans[0].linkCount(), "dispatch span MUST receive AddLink when tracelink is present")
	link := spans[0].linkAt(0)
	assert.Equal(t, tpMRTRLink, link.TraceContext.Traceparent,
		"AddLink's TraceContext.Traceparent MUST match the inbound tracelink (anchor of star semantic)")
}

// TestTraceMiddleware_NoTracelink_NoAddLink pins the zero-overhead
// path: tools/call without tracelink emits a span with NO AddLink
// calls. Critical for the dominant non-MRTR path — every regular
// tools/call MUST NOT incur a spurious link.
func TestTraceMiddleware_NoTracelink_NoAddLink(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo","_meta":{"traceparent":"` + tpInbound + `"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, 0, spans[0].linkCount(), "non-MRTR tools/call MUST NOT trigger AddLink")
}

// TestTraceMiddleware_MalformedTracelink_DroppedSilently pins the
// defensive contract: a malformed tracelink (e.g., garbage string)
// must NOT crash dispatch and MUST NOT produce a link. Same W3C
// validation rules ExtractTraceContext applies — silent drop.
func TestTraceMiddleware_MalformedTracelink_DroppedSilently(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo","_meta":{"io.modelcontextprotocol/tracelink":"not-a-valid-traceparent"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	spans := tp.snapshot()
	require.Len(t, spans, 1)
	assert.Equal(t, 0, spans[0].linkCount(), "malformed tracelink MUST be silently dropped — no AddLink")
}

// --- W3C Baggage propagation (sibling to trace context) -----------------

// TestTraceMiddleware_ExtractsBaggageFromMeta pins the inbound contract:
// `_meta.baggage` from the wire ends up on ctx where handlers can read
// it via core.BaggageFromContext.
func TestTraceMiddleware_ExtractsBaggageFromMeta(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))

	var observed core.Baggage
	srv.RegisterTool(core.ToolDef{Name: "peek"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		observed = ctx.Baggage()
		return core.TextResult("ok"), nil
	})

	params := `{"name":"peek","_meta":{"baggage":"userId=alice,tenant=acme"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, core.Baggage("userId=alice,tenant=acme"), observed,
		"handler MUST observe inbound _meta.baggage via ctx.Baggage()")
}

// TestTraceMiddleware_InjectsBaggageOnOutboundNotify pins the outbound
// contract: when the inbound request carries baggage, every outbound
// notification (server-to-client) emitted by the handler carries the
// same `_meta.baggage` value.
func TestTraceMiddleware_InjectsBaggageOnOutboundNotify(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := newInitializedServer(t, WithTracerProvider(tp))
	srv.RegisterTool(core.ToolDef{Name: "emit"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		ctx.Notify("notifications/test", map[string]any{"hello": "world"})
		return core.TextResult("ok"), nil
	})

	var captured []map[string]any
	notify := core.NotifyFunc(func(method string, params any) {
		raw, _ := json.Marshal(params)
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		captured = append(captured, obj)
	})

	params := `{"name":"emit","_meta":{"traceparent":"` + tpInbound + `","baggage":"userId=alice"}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, notify, nil)
	require.NoError(t, err)

	require.Len(t, captured, 1)
	meta, ok := captured[0]["_meta"].(map[string]any)
	require.True(t, ok, "outbound notification MUST carry _meta")
	assert.Equal(t, "userId=alice", meta[core.MetaKeyBaggage],
		"outbound notification _meta.baggage MUST mirror inbound")
}

// TestTraceMiddleware_BaggageHTTPHeaderBridge pins the SEP-2028
// inbound bridge for baggage: the HTTP `Baggage` header on a real
// POST lands on ctx alongside the trace context.
func TestTraceMiddleware_BaggageHTTPHeaderBridge(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := NewServer(core.ServerInfo{Name: "baggage-http", Version: "1.0"},
		WithTracerProvider(tp))

	var observed core.Baggage
	srv.RegisterTool(core.ToolDef{Name: "peek"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		observed = ctx.Baggage()
		return core.TextResult("ok"), nil
	})

	handler := srv.Handler(WithStreamableHTTP(true), WithStateless(true))

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"peek"}}`)
	r, err := http.NewRequest(http.MethodPost, "/mcp", body)
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set(httpHeaderBaggage, "userId=carol,tenant=widgets")

	w := newRespRecorder()
	handler.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.statusCode())
	assert.Equal(t, core.Baggage("userId=carol,tenant=widgets"), observed,
		"HTTP Baggage header MUST bridge into ctx.Baggage()")
}

// TestTraceMiddleware_InBandBaggageWinsOverHTTP pins the precedence
// rule: when both `_meta.baggage` and the HTTP `Baggage` header are
// present, the in-band value wins (mirror of the trace-context
// resolution rule).
func TestTraceMiddleware_InBandBaggageWinsOverHTTP(t *testing.T) {
	tp := &fakeTracerProvider{}
	srv := NewServer(core.ServerInfo{Name: "baggage-precedence", Version: "1.0"},
		WithTracerProvider(tp))

	var observed core.Baggage
	srv.RegisterTool(core.ToolDef{Name: "peek"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		observed = ctx.Baggage()
		return core.TextResult("ok"), nil
	})

	handler := srv.Handler(WithStreamableHTTP(true), WithStateless(true))

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"peek","_meta":{"baggage":"from=meta"}}}`)
	r, err := http.NewRequest(http.MethodPost, "/mcp", body)
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set(httpHeaderBaggage, "from=header")

	w := newRespRecorder()
	handler.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.statusCode())
	assert.Equal(t, core.Baggage("from=meta"), observed,
		"in-band _meta.baggage MUST win over HTTP Baggage header")
}
