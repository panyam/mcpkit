package server

// Issue 7 — dispatch-path metrics instrumentation.
//
// This file owns the `WithMeterProvider` option and the metrics
// middleware that records the canonical MCP server signals:
//
//   - mcp.tool.calls            — counter, label `tool` — every
//                                  successful tools/call dispatch.
//   - mcp.jsonrpc.errors        — counter, label `code` — every
//                                  JSON-RPC error response (resp.Error
//                                  != nil OR a returned error from
//                                  the handler chain).
//   - mcp.tool.duration         — float64 histogram, label `tool`,
//                                  unit `ms` — execution time of each
//                                  tools/call dispatch.
//   - mcp.sessions.active       — int64 up-down counter — current
//                                  count of live streamable HTTP
//                                  sessions. Owned by the transport
//                                  (see streamable_transport.go) but
//                                  the instrument is constructed here
//                                  so all metrics names live in one
//                                  file.
//
// Out of scope here:
//   - The ext/otel meter adapter that lets these flow to OTLP /
//     Prometheus — lives in ext/otel/meter.go.
//   - The examples/common.SetupMetrics helper that wires SetupTelemetry's
//     decision matrix for the meter side — issue 668 metrics half.

import (
	"context"
	"errors"
	"strconv"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// Canonical metric names shared with the SEP-414 trace attribute
// vocabulary (`mcp.method`, `mcp.tool.name`, `mcp.error.code`,
// `mcp.session.id`). Keeping the names in one block lets reviewers
// audit the public observability surface in a single read.
const (
	metricNameToolCalls      = "mcp.tool.calls"
	metricNameJSONRPCErrors  = "mcp.jsonrpc.errors"
	metricNameToolDuration   = "mcp.tool.duration"
	metricNameSessionsActive = "mcp.sessions.active"
)

// WithMeterProvider registers a core.MeterProvider that wraps every
// dispatched JSON-RPC request with canonical MCP metrics emission.
// Pair with WithTracerProvider to feed both Tempo and Mimir from a
// single ext/otel adapter (see commonotel.SetupMetrics — issue 668).
//
// When nil or core.NoopMeterProvider{} is passed, no metrics
// middleware is installed — zero overhead on the default path. The
// default (no Option set) is identical to passing Noop.
//
// The metrics middleware is installed one layer INSIDE the SEP-414
// trace middleware so the `mcp.tool.duration` histogram captures
// handler-attributable latency, not span-emission overhead. The
// trace span still wraps the metrics middleware so any overhead is
// visible in Tempo.
//
// Active-session counting (mcp.sessions.active) is wired into the
// streamable HTTP transport at session-create / session-expire
// boundaries — see Server.RecordSessionDelta. Stateless wire has no
// sessions so the gauge stays at zero on stateless-only deployments
// (correct).
func WithMeterProvider(mp core.MeterProvider) Option {
	return func(o *serverOptions) {
		o.meterProvider = mp
	}
}

// metricsEnabled reports whether the configured MeterProvider should
// install the dispatch metrics middleware. nil and the default
// core.NoopMeterProvider both report false so the zero-overhead path
// is preserved when no metrics are wired.
//
// Sibling to tracingEnabled — identical contract.
func metricsEnabled(mp core.MeterProvider) bool {
	if mp == nil {
		return false
	}
	if _, isNoop := mp.(core.NoopMeterProvider); isNoop {
		return false
	}
	return true
}

// newMetricsMiddleware constructs the per-request middleware that
// records mcp.tool.calls / mcp.tool.duration / mcp.jsonrpc.errors
// against the supplied MeterProvider. Instruments are created once
// (at NewServer) and captured by the middleware closure so the hot
// path costs one closure call + three Add/Record forwards.
//
// Caller MUST pass mp != nil; the install layer gates via
// metricsEnabled before reaching here.
func newMetricsMiddleware(mp core.MeterProvider) Middleware {
	toolCalls := mp.Int64Counter(metricNameToolCalls,
		core.WithDescription("Number of tools/call requests dispatched, broken down by tool name."),
		core.WithUnit("1"),
	)
	jsonrpcErrors := mp.Int64Counter(metricNameJSONRPCErrors,
		core.WithDescription("Number of JSON-RPC error responses emitted by the server, broken down by error code."),
		core.WithUnit("1"),
	)
	toolDuration := mp.Float64Histogram(metricNameToolDuration,
		core.WithDescription("Handler-attributable execution time for tools/call requests, broken down by tool name."),
		core.WithUnit("ms"),
	)

	return func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		isToolCall := req.Method == "tools/call"
		var toolName string
		var start time.Time
		if isToolCall {
			toolName = parseToolCallName(req.Params)
			start = time.Now()
		}

		resp, err := next(ctx, req)

		if isToolCall {
			toolAttr := core.Attribute{Key: "tool", Value: toolName}
			toolCalls.Add(ctx, 1, toolAttr)
			toolDuration.Record(ctx, float64(time.Since(start).Microseconds())/1000.0, toolAttr)
		}
		recordJSONRPCError(ctx, jsonrpcErrors, resp, err)
		return resp, err
	}
}

// recordJSONRPCError increments the JSON-RPC error counter when the
// dispatch ended in a JSON-RPC protocol error (resp.Error != nil) or
// a transport-level error (err != nil). Sibling to
// trace_middleware.go::recordSpanOutcome — same three layers, same
// priority order:
//
//   - Transport-level error: code attribute `-32603` (Internal error,
//     since the transport will surface this as HTTP 500 / auth
//     challenge / etc.).
//   - JSON-RPC protocol error: code attribute from resp.Error.Code.
//   - Tool-level isError: NOT counted as a JSON-RPC error. Per spec
//     these are successful tools/call responses; the trace span
//     stamps `mcp.tool.is_error="true"` for that case and operators
//     can derive a tool-error rate from the trace search.
func recordJSONRPCError(ctx context.Context, c core.Int64Counter, resp *core.Response, err error) {
	if err != nil {
		c.Add(ctx, 1, core.Attribute{Key: "code", Value: strconv.Itoa(jsonRPCInternalError)})
		return
	}
	if resp == nil || resp.Error == nil {
		return
	}
	// Defensive against a nil-but-non-nil-interface trap that the
	// upstream callers (handleToolsCall + friends) don't produce —
	// but the explicit guard keeps the helper safe to relocate.
	if errors.Is(err, nil) && resp.Error == nil {
		return
	}
	c.Add(ctx, 1, core.Attribute{Key: "code", Value: strconv.Itoa(resp.Error.Code)})
}

// jsonRPCInternalError is the JSON-RPC reserved code surfaced when
// the dispatch returned a transport-level error rather than a
// protocol-level one. Matches the convention RFC 7049 / JSON-RPC 2.0
// reserves for "Internal error".
const jsonRPCInternalError = -32603

// newSessionsActiveCounter constructs the up-down counter the
// streamable HTTP transport uses for mcp.sessions.active. Exposed at
// package scope (rather than baked into newMetricsMiddleware) because
// the transport accesses it independently of the per-request
// middleware — session create / expire happen outside the dispatch
// loop.
func newSessionsActiveCounter(mp core.MeterProvider) core.Int64UpDownCounter {
	return mp.Int64UpDownCounter(metricNameSessionsActive,
		core.WithDescription("Currently active streamable HTTP sessions on this server instance."),
		core.WithUnit("1"),
	)
}

// RecordSessionDelta lets transports report session-lifecycle deltas
// to the metrics seam without each transport carrying a MeterProvider
// of its own. delta is +1 on session-create, -1 on session-expire /
// DELETE.
//
// No-op when metrics are disabled (s.sessionsActive is nil) — the
// transport calls this unconditionally and the metrics install
// state decides whether anything happens. Hot-path cost on the
// disabled branch is one nil-check.
func (s *Server) RecordSessionDelta(ctx context.Context, delta int64) {
	if s.sessionsActive == nil {
		return
	}
	s.sessionsActive.Add(ctx, delta)
}
