package otel_test

import (
	"context"
	"testing"

	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNewTracerProvider_NilExporter_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = mcpotel.NewTracerProvider(nil)
	})
}

func TestNewTracerProvider_NoOpts_BuildsValidTP(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := mcpotel.NewTracerProvider(exp, mcpotel.WithSyncer())
	require.NotNil(t, tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "span-without-service-name")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	// Resource attributes vary by SDK default; we just assert the
	// span was recorded without panicking.
	assert.Equal(t, "span-without-service-name", spans[0].Name)
}

func TestWithServiceName_FlowsToRecordedResource(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := mcpotel.NewTracerProvider(exp,
		mcpotel.WithServiceName("my-test-service"),
		mcpotel.WithSyncer(),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "span-with-service-name")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)

	var serviceName string
	for _, kv := range spans[0].Resource.Attributes() {
		if string(kv.Key) == "service.name" {
			serviceName = kv.Value.AsString()
		}
	}
	assert.Equal(t, "my-test-service", serviceName,
		"WithServiceName must set the OTel Resource service.name attribute the SDK exporter records")
}

func TestWithServiceName_Empty_NoOp(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := mcpotel.NewTracerProvider(exp,
		mcpotel.WithServiceName(""),
		mcpotel.WithSyncer(),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "empty-service-name-span")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	for _, kv := range spans[0].Resource.Attributes() {
		// SDK default Resource sets service.name to something like
		// "unknown_service:<binary>" — the absence of our value is
		// what we're asserting, not the absence of any value.
		assert.NotEqual(t, "my-test-service", kv.Value.AsString())
	}
}

func TestWithSyncer_EmitsImmediately(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := mcpotel.NewTracerProvider(exp,
		mcpotel.WithServiceName("sync-test"),
		mcpotel.WithSyncer(),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "sync-span")
	span.End()

	// No explicit ForceFlush — sync processor must have already
	// pushed the span to the exporter by the time End() returned.
	assert.Len(t, exp.GetSpans(), 1,
		"WithSyncer must push spans on End() without an explicit ForceFlush — the test would fail with WithBatcher (default)")
}

func TestNewTracerProvider_DefaultsToBatcher(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := mcpotel.NewTracerProvider(exp,
		mcpotel.WithServiceName("batcher-test"),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "batched-span")
	span.End()

	// Without WithSyncer, the batch processor holds the span — it
	// hasn't flushed yet, so the exporter is empty. ForceFlush
	// drains it.
	assert.Empty(t, exp.GetSpans(), "default Batcher must hold spans until ForceFlush")

	require.NoError(t, tp.ForceFlush(context.Background()))
	assert.Len(t, exp.GetSpans(), 1)
}

// Sanity: the helper returns a *sdktrace.TracerProvider that can be
// composed with the existing mcpotel.NewProvider wrapper.
func TestNewTracerProvider_ComposesWithNewProvider(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := mcpotel.NewTracerProvider(exp,
		mcpotel.WithServiceName("compose-test"),
		mcpotel.WithSyncer(),
	)
	require.NotNil(t, tp)
	// Just confirm the type is what NewProvider expects.
	var _ sdktrace.SpanExporter = exp
	provider := mcpotel.NewProvider(tp)
	require.NotNil(t, provider)
}
