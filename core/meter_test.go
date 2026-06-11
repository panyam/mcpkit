package core_test

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// Compile-time assertions that NoopMeterProvider and its instruments
// satisfy the public interfaces. A future contract change that breaks
// the seam will surface here at compile time, before any production
// call site reports a runtime panic.
var (
	_ core.MeterProvider       = core.NoopMeterProvider{}
	_ core.Int64Counter        = core.NoopMeterProvider{}.Int64Counter("smoke")
	_ core.Float64Histogram    = core.NoopMeterProvider{}.Float64Histogram("smoke")
	_ core.Int64UpDownCounter  = core.NoopMeterProvider{}.Int64UpDownCounter("smoke")
)

func TestNoopMeterProvider_InstrumentsAreNonNil(t *testing.T) {
	mp := core.NoopMeterProvider{}
	if mp.Int64Counter("c") == nil {
		t.Fatalf("Int64Counter must return non-nil even on Noop path — call sites do not nil-check")
	}
	if mp.Float64Histogram("h") == nil {
		t.Fatalf("Float64Histogram must return non-nil even on Noop path")
	}
	if mp.Int64UpDownCounter("ud") == nil {
		t.Fatalf("Int64UpDownCounter must return non-nil even on Noop path")
	}
}

func TestNoopMeterProvider_MeasurementsAreNoOps(t *testing.T) {
	mp := core.NoopMeterProvider{}
	ctx := context.Background()
	mp.Int64Counter("c").Add(ctx, 42, core.Attribute{Key: "k", Value: "v"})
	mp.Float64Histogram("h").Record(ctx, 1.23, core.Attribute{Key: "k", Value: "v"})
	mp.Int64UpDownCounter("ud").Add(ctx, -7)
	// No assertions — the test verifies non-panic. Noop must accept
	// every shape the real adapter accepts without crashing so
	// dispatch-path code can call the same API on both branches.
}

func TestNoopMeterProvider_AcceptsAllOptions(t *testing.T) {
	mp := core.NoopMeterProvider{}
	c := mp.Int64Counter("c",
		core.WithDescription("test counter"),
		core.WithUnit("1"),
	)
	c.Add(context.Background(), 1)
	// Noop ignores options; the test asserts the construction call
	// itself is valid so call sites can carry options across the
	// Noop / real branch.
}

func TestInstrumentOptions_MergeOrder(t *testing.T) {
	cfg := core.ApplyInstrumentOptions(
		core.WithDescription("first"),
		core.WithUnit("ms"),
		core.WithDescription("second"),
	)
	if cfg.Description != "second" {
		t.Fatalf("last WithDescription must win; got %q", cfg.Description)
	}
	if cfg.Unit != "ms" {
		t.Fatalf("Unit should survive subsequent unrelated options; got %q", cfg.Unit)
	}
}

func TestInstrumentOptions_EmptyValuesAreNoOps(t *testing.T) {
	// Empty WithDescription / WithUnit must not clobber a previously
	// set value — matches the "explicit beats env" convention from
	// SetupTelemetry where an empty option is treated as "no
	// preference expressed".
	cfg := core.ApplyInstrumentOptions(
		core.WithDescription("set"),
		core.WithUnit("By"),
		core.WithDescription(""),
		core.WithUnit(""),
	)
	if cfg.Description != "set" {
		t.Fatalf("empty WithDescription must not clobber; got %q", cfg.Description)
	}
	if cfg.Unit != "By" {
		t.Fatalf("empty WithUnit must not clobber; got %q", cfg.Unit)
	}
}

func TestInstrumentOptions_ZeroValueIsValid(t *testing.T) {
	// ApplyInstrumentOptions with no args returns a zero
	// InstrumentConfig — a perfectly valid instrument with no
	// description and no unit. Adapters must accept this without
	// special-casing.
	cfg := core.ApplyInstrumentOptions()
	if cfg.Description != "" || cfg.Unit != "" {
		t.Fatalf("zero-args ApplyInstrumentOptions must produce zero InstrumentConfig; got %+v", cfg)
	}
}
