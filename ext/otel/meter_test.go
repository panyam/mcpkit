package otel_test

// Issue 7 — ext/otel meter adapter tests. Drives the adapter through
// a real go.opentelemetry.io/otel/sdk/metric pipeline with a
// ManualReader so assertions can read back actual metricdata.Metrics
// snapshots — the SDK is the spec.

import (
	"context"
	"testing"

	core "github.com/panyam/mcpkit/core"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newRecordingMeter constructs a real OTel MeterProvider with a
// ManualReader so the suite can pull metric snapshots synchronously
// without spinning up an exporter. Returns the seam-wrapped
// MeterProvider + a collect closure that produces a fresh
// metricdata.ResourceMetrics each call.
func newRecordingMeter(t *testing.T) (*mcpotel.MeterProvider, func() metricdata.ResourceMetrics) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	sdkMP := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = sdkMP.Shutdown(context.Background())
	})
	collect := func() metricdata.ResourceMetrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("reader.Collect: %v", err)
		}
		return rm
	}
	return mcpotel.NewMeterProvider(sdkMP), collect
}

// findScopeMetrics returns the scope metrics block matching name (the
// instrumentation library label the adapter stamps). Tests use this
// to fetch the mcpkit-emitted instruments out of a full ResourceMetrics
// snapshot.
func findScopeMetrics(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.ScopeMetrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name == name {
			return sm
		}
	}
	t.Fatalf("no scope metrics named %q in ResourceMetrics; got %v", name, scopeNames(rm))
	return metricdata.ScopeMetrics{}
}

func scopeNames(rm metricdata.ResourceMetrics) []string {
	out := make([]string, 0, len(rm.ScopeMetrics))
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Scope.Name)
	}
	return out
}

// findMetric returns the metric with the given name from a scope's
// Metrics list, or nil if absent.
func findMetric(metrics []metricdata.Metrics, name string) *metricdata.Metrics {
	for i := range metrics {
		if metrics[i].Name == name {
			return &metrics[i]
		}
	}
	return nil
}

// --- construction ------------------------------------------------------------

func TestNewMeterProvider_NilOtelMP_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = mcpotel.NewMeterProvider(nil)
	})
}

func TestNewMeterProvider_DefaultInstrumentationName(t *testing.T) {
	mp, collect := newRecordingMeter(t)
	mp.Int64Counter("test.counter").Add(context.Background(), 1)

	rm := collect()
	sm := findScopeMetrics(t, rm, "github.com/panyam/mcpkit/server")
	assert.NotEmpty(t, sm.Metrics, "default instrumentation name must produce a non-empty scope")
}

func TestNewMeterProvider_CustomInstrumentationName(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	sdkMP := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = sdkMP.Shutdown(context.Background()) })

	mp := mcpotel.NewMeterProvider(sdkMP, mcpotel.WithMeterInstrumentationName("custom-lib"))
	mp.Int64Counter("test.counter").Add(context.Background(), 1)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	_ = findScopeMetrics(t, rm, "custom-lib")
}

// --- counter -----------------------------------------------------------------

func TestInt64Counter_RecordsValuesAndAttributes(t *testing.T) {
	mp, collect := newRecordingMeter(t)
	c := mp.Int64Counter("test.counter",
		core.WithDescription("test description"),
		core.WithUnit("1"),
	)
	ctx := context.Background()
	c.Add(ctx, 1, core.Attribute{Key: "tool", Value: "alpha"})
	c.Add(ctx, 1, core.Attribute{Key: "tool", Value: "alpha"})
	c.Add(ctx, 1, core.Attribute{Key: "tool", Value: "beta"})

	rm := collect()
	sm := findScopeMetrics(t, rm, "github.com/panyam/mcpkit/server")
	m := findMetric(sm.Metrics, "test.counter")
	require.NotNil(t, m, "counter must appear in scope metrics; got %v", metricNames(sm.Metrics))
	assert.Equal(t, "test description", m.Description)
	assert.Equal(t, "1", m.Unit)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "Int64Counter must produce metricdata.Sum[int64]")
	require.Len(t, sum.DataPoints, 2, "counter should bucket by attribute set; alpha + beta")

	alpha, beta := findDataPointByAttr(sum.DataPoints, "tool", "alpha"), findDataPointByAttr(sum.DataPoints, "tool", "beta")
	require.NotNil(t, alpha)
	require.NotNil(t, beta)
	assert.Equal(t, int64(2), alpha.Value)
	assert.Equal(t, int64(1), beta.Value)
}

// --- histogram ---------------------------------------------------------------

func TestFloat64Histogram_RecordsValuesAndAttributes(t *testing.T) {
	mp, collect := newRecordingMeter(t)
	h := mp.Float64Histogram("test.duration",
		core.WithDescription("test latency"),
		core.WithUnit("ms"),
	)
	ctx := context.Background()
	h.Record(ctx, 1.5, core.Attribute{Key: "tool", Value: "alpha"})
	h.Record(ctx, 2.5, core.Attribute{Key: "tool", Value: "alpha"})

	rm := collect()
	sm := findScopeMetrics(t, rm, "github.com/panyam/mcpkit/server")
	m := findMetric(sm.Metrics, "test.duration")
	require.NotNil(t, m)
	assert.Equal(t, "test latency", m.Description)
	assert.Equal(t, "ms", m.Unit)

	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "Float64Histogram must produce metricdata.Histogram[float64]")
	require.Len(t, hist.DataPoints, 1)
	dp := hist.DataPoints[0]
	assert.Equal(t, uint64(2), dp.Count, "histogram must count both Record calls")
	assert.Equal(t, 4.0, dp.Sum, "histogram sum must accumulate both observations")
}

// --- up-down counter ---------------------------------------------------------

func TestInt64UpDownCounter_RecordsDeltas(t *testing.T) {
	mp, collect := newRecordingMeter(t)
	c := mp.Int64UpDownCounter("test.gauge",
		core.WithDescription("active things"),
		core.WithUnit("1"),
	)
	ctx := context.Background()
	c.Add(ctx, 5)
	c.Add(ctx, 3)
	c.Add(ctx, -2)

	rm := collect()
	sm := findScopeMetrics(t, rm, "github.com/panyam/mcpkit/server")
	m := findMetric(sm.Metrics, "test.gauge")
	require.NotNil(t, m)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "Int64UpDownCounter must produce metricdata.Sum[int64]")
	assert.False(t, sum.IsMonotonic, "up-down counter must not be monotonic")
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(6), sum.DataPoints[0].Value, "net delta should be 5+3-2 = 6")
}

// --- adapter glue ------------------------------------------------------------

func TestOTelMeterProvider_ReturnsBackingProvider(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	sdkMP := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = sdkMP.Shutdown(context.Background()) })

	mp := mcpotel.NewMeterProvider(sdkMP)
	if mp.OTelMeterProvider() != sdkMP {
		t.Fatalf("OTelMeterProvider() must return the pointer passed to NewMeterProvider")
	}
}

func TestInstrumentSatisfiesCoreInterface(t *testing.T) {
	// Compile-time assertion via runtime — the adapter's instrument
	// implementations satisfy the core seam contract. A signature
	// drift would surface here as a compile error.
	mp, _ := newRecordingMeter(t)
	var _ core.Int64Counter = mp.Int64Counter("smoke")
	var _ core.Float64Histogram = mp.Float64Histogram("smoke-hist")
	var _ core.Int64UpDownCounter = mp.Int64UpDownCounter("smoke-ud")
}

// --- helpers -----------------------------------------------------------------

func metricNames(ms []metricdata.Metrics) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

func findDataPointByAttr(dps []metricdata.DataPoint[int64], key, value string) *metricdata.DataPoint[int64] {
	for i := range dps {
		if v, ok := dps[i].Attributes.Value(attribute.Key(key)); ok && v.AsString() == value {
			return &dps[i]
		}
	}
	return nil
}
