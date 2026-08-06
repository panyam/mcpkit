package main

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestSetupMeter_EmptyExporter_ReturnsNoop(t *testing.T) {
	mp, shutdown, err := SetupMeter(context.Background())
	if err != nil {
		t.Fatalf("SetupMeter: %v", err)
	}
	if _, ok := mp.(core.NoopMeterProvider); !ok {
		t.Fatalf("Exporter=\"\" must return core.NoopMeterProvider; got %T", mp)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown on Noop path must be no-op; got %v", err)
	}
}

func TestSetupMeter_UnknownExporter_ReturnsError(t *testing.T) {
	mp, shutdown, err := SetupMeter(context.Background(), WithExporter("bogus-mode"))
	if err == nil {
		t.Fatalf("unknown exporter must error so a typo doesn't silently turn metrics off")
	}
	if mp != nil || shutdown != nil {
		t.Fatalf("error return must hand back nil provider + nil shutdown; got mp=%v shutdown=%v", mp, shutdown)
	}
	if !strings.Contains(err.Error(), "bogus-mode") {
		t.Fatalf("error message should name the bad exporter; got %v", err)
	}
}

func TestSetupMeter_Stdout_BuildsRealProvider(t *testing.T) {
	mp, shutdown, err := SetupMeter(context.Background(),
		WithExporter(ExporterStdout),
		WithServiceName("setup-meter-test-stdout"),
	)
	if err != nil {
		t.Fatalf("SetupMeter: %v", err)
	}
	defer shutdown(context.Background())
	if _, ok := mp.(core.NoopMeterProvider); ok {
		t.Fatalf("stdout exporter must build a real MeterProvider, not the Noop")
	}
	// Instruments must be usable (the Runner records through them).
	mp.Int64Counter("agent.turns").Add(context.Background(), 1)
	mp.Float64Histogram("agent.turn.duration").Record(context.Background(), 0.5)
}
