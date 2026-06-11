package server

// Issue 7 — dispatch-path metrics middleware tests.
//
// White-box (`package server`) so the suite can wire the dispatcher
// directly via dispatchWithNotifyAndRequest, matching the trace
// middleware test pattern. Uses a fake MeterProvider that captures
// every Add/Record call — the OTel SDK-backed coverage lives in
// ext/otel/meter_test.go (live SDK + metricdata reader).

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	core "github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fake MeterProvider ------------------------------------------------------

// recordedMeasurement captures one Add or Record call's metadata so
// individual tests can assert on attribute values + counts without
// needing a live OTel SDK.
type recordedMeasurement struct {
	value any // int64 for counters, float64 for histograms
	attrs []core.Attribute
}

type fakeCounter struct {
	mu           sync.Mutex
	measurements []recordedMeasurement
}

func (c *fakeCounter) Add(_ context.Context, v int64, attrs ...core.Attribute) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.measurements = append(c.measurements, recordedMeasurement{
		value: v,
		attrs: append([]core.Attribute(nil), attrs...),
	})
}

func (c *fakeCounter) snapshot() []recordedMeasurement {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]recordedMeasurement, len(c.measurements))
	copy(out, c.measurements)
	return out
}

type fakeHistogram struct {
	mu           sync.Mutex
	measurements []recordedMeasurement
}

func (h *fakeHistogram) Record(_ context.Context, v float64, attrs ...core.Attribute) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.measurements = append(h.measurements, recordedMeasurement{
		value: v,
		attrs: append([]core.Attribute(nil), attrs...),
	})
}

func (h *fakeHistogram) snapshot() []recordedMeasurement {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]recordedMeasurement, len(h.measurements))
	copy(out, h.measurements)
	return out
}

type fakeUpDownCounter struct {
	mu           sync.Mutex
	measurements []recordedMeasurement
}

func (c *fakeUpDownCounter) Add(_ context.Context, v int64, attrs ...core.Attribute) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.measurements = append(c.measurements, recordedMeasurement{
		value: v,
		attrs: append([]core.Attribute(nil), attrs...),
	})
}

func (c *fakeUpDownCounter) snapshot() []recordedMeasurement {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]recordedMeasurement, len(c.measurements))
	copy(out, c.measurements)
	return out
}

type fakeMeterProvider struct {
	mu                sync.Mutex
	counters          map[string]*fakeCounter
	histograms        map[string]*fakeHistogram
	upDownCounters    map[string]*fakeUpDownCounter
	instrumentConfigs map[string]core.InstrumentConfig
}

func newFakeMeter() *fakeMeterProvider {
	return &fakeMeterProvider{
		counters:          map[string]*fakeCounter{},
		histograms:        map[string]*fakeHistogram{},
		upDownCounters:    map[string]*fakeUpDownCounter{},
		instrumentConfigs: map[string]core.InstrumentConfig{},
	}
}

func (m *fakeMeterProvider) Int64Counter(name string, opts ...core.InstrumentOption) core.Int64Counter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.counters[name]; ok {
		return c
	}
	m.instrumentConfigs[name] = core.ApplyInstrumentOptions(opts...)
	c := &fakeCounter{}
	m.counters[name] = c
	return c
}

func (m *fakeMeterProvider) Float64Histogram(name string, opts ...core.InstrumentOption) core.Float64Histogram {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.histograms[name]; ok {
		return h
	}
	m.instrumentConfigs[name] = core.ApplyInstrumentOptions(opts...)
	h := &fakeHistogram{}
	m.histograms[name] = h
	return h
}

func (m *fakeMeterProvider) Int64UpDownCounter(name string, opts ...core.InstrumentOption) core.Int64UpDownCounter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.upDownCounters[name]; ok {
		return c
	}
	m.instrumentConfigs[name] = core.ApplyInstrumentOptions(opts...)
	c := &fakeUpDownCounter{}
	m.upDownCounters[name] = c
	return c
}

// --- install-gate tests ------------------------------------------------------

func TestMetrics_NoProviderInstallsNothing(t *testing.T) {
	srv := newInitializedServer(t)
	if srv.metricsMiddleware != nil {
		t.Fatalf("default config must not install metrics middleware; got non-nil")
	}
	if srv.sessionsActive != nil {
		t.Fatalf("default config must not install sessions counter; got non-nil")
	}
}

func TestMetrics_NoopProviderInstallsNothing(t *testing.T) {
	srv := newInitializedServer(t, WithMeterProvider(core.NoopMeterProvider{}))
	if srv.metricsMiddleware != nil {
		t.Fatalf("Noop provider must not install metrics middleware; got non-nil")
	}
	if srv.sessionsActive != nil {
		t.Fatalf("Noop provider must not install sessions counter; got non-nil")
	}
}

func TestMetrics_RealProviderInstallsMiddleware(t *testing.T) {
	mp := newFakeMeter()
	srv := newInitializedServer(t, WithMeterProvider(mp))
	if srv.metricsMiddleware == nil {
		t.Fatalf("real provider must install metrics middleware; got nil")
	}
	if srv.sessionsActive == nil {
		t.Fatalf("real provider must install sessions counter; got nil")
	}
	// Sessions counter must exist with the canonical name.
	if _, ok := mp.upDownCounters[metricNameSessionsActive]; !ok {
		t.Fatalf("provider must be asked for %q at install time; got %v", metricNameSessionsActive, mp.upDownCounters)
	}
}

// --- tools/call instrumentation ---------------------------------------------

func TestMetrics_ToolCall_RecordsCounterAndDuration(t *testing.T) {
	mp := newFakeMeter()
	srv := newInitializedServer(t, WithMeterProvider(mp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	params := `{"name":"echo","arguments":{}}`
	_, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)

	calls := mp.counters[metricNameToolCalls].snapshot()
	require.Len(t, calls, 1, "every tools/call dispatch records exactly one mcp.tool.calls increment")
	assert.Equal(t, int64(1), calls[0].value)
	require.Len(t, calls[0].attrs, 1)
	assert.Equal(t, core.Attribute{Key: "tool", Value: "echo"}, calls[0].attrs[0])

	durations := mp.histograms[metricNameToolDuration].snapshot()
	require.Len(t, durations, 1, "every tools/call dispatch records exactly one mcp.tool.duration observation")
	assert.GreaterOrEqual(t, durations[0].value.(float64), 0.0, "histogram must record a non-negative duration")
	require.Len(t, durations[0].attrs, 1)
	assert.Equal(t, core.Attribute{Key: "tool", Value: "echo"}, durations[0].attrs[0])
}

func TestMetrics_NonToolCall_SkipsToolMetrics(t *testing.T) {
	mp := newFakeMeter()
	srv := newInitializedServer(t, WithMeterProvider(mp))
	srv.RegisterTool(core.ToolDef{Name: "echo"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/list",
		Params: json.RawMessage(`{}`),
	}
	_, err := srv.dispatchWithNotifyAndRequest(srv.dispatcher, context.Background(), nil, nil, nil, req)
	require.NoError(t, err)

	if c, ok := mp.counters[metricNameToolCalls]; ok {
		assert.Empty(t, c.snapshot(), "tools/list must NOT increment mcp.tool.calls (tool-scoped counter)")
	}
	if h, ok := mp.histograms[metricNameToolDuration]; ok {
		assert.Empty(t, h.snapshot(), "tools/list must NOT record mcp.tool.duration")
	}
}

func TestMetrics_ToolCall_DistinguishesByName(t *testing.T) {
	mp := newFakeMeter()
	srv := newInitializedServer(t, WithMeterProvider(mp))
	srv.RegisterTool(core.ToolDef{Name: "alpha"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("a"), nil
	})
	srv.RegisterTool(core.ToolDef{Name: "beta"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("b"), nil
	})

	_, err := dispatchToolsCall(t, srv, context.Background(), `{"name":"alpha","arguments":{}}`, nil, nil)
	require.NoError(t, err)
	_, err = dispatchToolsCall(t, srv, context.Background(), `{"name":"beta","arguments":{}}`, nil, nil)
	require.NoError(t, err)
	_, err = dispatchToolsCall(t, srv, context.Background(), `{"name":"alpha","arguments":{}}`, nil, nil)
	require.NoError(t, err)

	calls := mp.counters[metricNameToolCalls].snapshot()
	require.Len(t, calls, 3)
	toolNames := []string{
		calls[0].attrs[0].Value,
		calls[1].attrs[0].Value,
		calls[2].attrs[0].Value,
	}
	assert.Equal(t, []string{"alpha", "beta", "alpha"}, toolNames,
		"each call labels with the tool name as it dispatched — operator can build per-tool rates")
}

// --- JSON-RPC error counter -------------------------------------------------

func TestMetrics_JSONRPCError_RecordsCodeAttribute(t *testing.T) {
	mp := newFakeMeter()
	srv := newInitializedServer(t, WithMeterProvider(mp))
	// No tool registered — tools/call should produce JSON-RPC -32602
	// (invalid params) or -32601 (method not found) depending on the
	// dispatch path. Either way, the error counter increments with a
	// non-zero code.
	params := `{"name":"missing","arguments":{}}`
	resp, err := dispatchToolsCall(t, srv, context.Background(), params, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error, "missing tool must produce a JSON-RPC error response")

	errors := mp.counters[metricNameJSONRPCErrors].snapshot()
	require.Len(t, errors, 1)
	assert.Equal(t, int64(1), errors[0].value)
	require.Len(t, errors[0].attrs, 1)
	assert.Equal(t, "code", errors[0].attrs[0].Key)
	// The exact code is dispatch-implementation-defined; the contract
	// the metric promises is "the code attribute carries SOME
	// JSON-RPC error code". Asserting on the precise code would
	// couple this test to handleToolsCall's internal error mapping.
	assert.NotEmpty(t, errors[0].attrs[0].Value, "code attribute must be populated")
}

func TestMetrics_TransportError_RecordsInternalErrorCode(t *testing.T) {
	mp := newFakeMeter()
	mw := newMetricsMiddleware(mp)

	// Bypass the dispatcher entirely — feed the middleware a chain
	// that errors out at the transport layer, simulating a custom
	// middleware returning a *core.AuthError or rate-limit error.
	transportErr := errors.New("simulated transport error")
	resp, err := mw(context.Background(), &core.Request{
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"x"}`),
	}, func(context.Context, *core.Request) (*core.Response, error) {
		return nil, transportErr
	})
	assert.Nil(t, resp)
	assert.Equal(t, transportErr, err)

	errors := mp.counters[metricNameJSONRPCErrors].snapshot()
	require.Len(t, errors, 1)
	assert.Equal(t, "code", errors[0].attrs[0].Key)
	assert.Equal(t, "-32603", errors[0].attrs[0].Value,
		"transport-level errors map to JSON-RPC Internal Error (-32603)")
}

func TestMetrics_ToolErrorResult_IsNotJSONRPCError(t *testing.T) {
	// Per SEP-414 / SEP-2243 semantics: a ToolResult{IsError:true} is
	// a SUCCESSFUL JSON-RPC response. The trace span stamps
	// `mcp.tool.is_error="true"` for the case; mcp.jsonrpc.errors
	// MUST NOT fire because the request itself didn't fail.
	mp := newFakeMeter()
	srv := newInitializedServer(t, WithMeterProvider(mp))
	srv.RegisterTool(core.ToolDef{Name: "failing"}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.ToolResult{IsError: true, Content: []core.Content{{Type: "text", Text: "simulated"}}}, nil
	})

	resp, err := dispatchToolsCall(t, srv, context.Background(), `{"name":"failing","arguments":{}}`, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Nil(t, resp.Error, "tool-level error is NOT a JSON-RPC error — Response.Error must be nil")

	if c, ok := mp.counters[metricNameJSONRPCErrors]; ok {
		assert.Empty(t, c.snapshot(),
			"ToolResult{IsError:true} must NOT increment mcp.jsonrpc.errors — that signal is reserved for protocol-level failures")
	}
	// The tool call itself still counts toward mcp.tool.calls.
	calls := mp.counters[metricNameToolCalls].snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "failing", calls[0].attrs[0].Value)
}

// --- sessions-active counter -------------------------------------------------

func TestMetrics_RecordSessionDelta_UpdatesCounter(t *testing.T) {
	mp := newFakeMeter()
	srv := newInitializedServer(t, WithMeterProvider(mp))

	srv.RecordSessionDelta(context.Background(), +1)
	srv.RecordSessionDelta(context.Background(), +1)
	srv.RecordSessionDelta(context.Background(), -1)

	deltas := mp.upDownCounters[metricNameSessionsActive].snapshot()
	require.Len(t, deltas, 3, "every RecordSessionDelta call must reach the underlying instrument")
	assert.Equal(t, int64(1), deltas[0].value)
	assert.Equal(t, int64(1), deltas[1].value)
	assert.Equal(t, int64(-1), deltas[2].value)
}

func TestMetrics_RecordSessionDelta_NoopWhenDisabled(t *testing.T) {
	// No WithMeterProvider — RecordSessionDelta must be a safe
	// no-op so the streamable transport can call it unconditionally.
	srv := newInitializedServer(t)
	// If this panicked, the transport would crash on every session
	// create. The non-panic itself is the assertion.
	srv.RecordSessionDelta(context.Background(), +1)
	srv.RecordSessionDelta(context.Background(), -1)
}

// --- instrument metadata -----------------------------------------------------

func TestMetrics_InstrumentMetadata_RecordedAtInstall(t *testing.T) {
	mp := newFakeMeter()
	_ = newInitializedServer(t, WithMeterProvider(mp))

	// Each canonical instrument was constructed with a non-empty
	// description and unit — the contract WithMeterProvider promises
	// observability backends.
	for _, name := range []string{
		metricNameToolCalls,
		metricNameJSONRPCErrors,
		metricNameToolDuration,
		metricNameSessionsActive,
	} {
		cfg, ok := mp.instrumentConfigs[name]
		require.True(t, ok, "instrument %q must be constructed at NewServer", name)
		assert.NotEmpty(t, cfg.Description, "instrument %q must carry a description", name)
		assert.NotEmpty(t, cfg.Unit, "instrument %q must carry a unit", name)
	}

	// mcp.tool.duration MUST be in milliseconds — the contract
	// SetupMetrics + Grafana dashboards rely on.
	assert.Equal(t, "ms", mp.instrumentConfigs[metricNameToolDuration].Unit,
		"mcp.tool.duration must be ms — dashboards/queries assume it")
}
