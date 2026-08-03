package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// recMeter is a recording MeterProvider for the tests: every Add / Record lands
// as a point on the shared recorder, tagged with its instrument name and
// attributes, so a test can assert exactly what the Runner emitted.
type recPoint struct {
	name  string
	value float64
	attrs map[string]string
}

type recMeter struct {
	mu     sync.Mutex
	points []recPoint
}

func (m *recMeter) record(name string, v float64, attrs []core.Attribute) {
	am := map[string]string{}
	for _, a := range attrs {
		am[a.Key] = a.Value
	}
	m.mu.Lock()
	m.points = append(m.points, recPoint{name: name, value: v, attrs: am})
	m.mu.Unlock()
}

func (m *recMeter) named(name string) []recPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []recPoint
	for _, p := range m.points {
		if p.name == name {
			out = append(out, p)
		}
	}
	return out
}

func (m *recMeter) Int64Counter(name string, _ ...core.InstrumentOption) core.Int64Counter {
	return recInstrument{name: name, m: m}
}
func (m *recMeter) Float64Histogram(name string, _ ...core.InstrumentOption) core.Float64Histogram {
	return recInstrument{name: name, m: m}
}
func (m *recMeter) Int64UpDownCounter(name string, _ ...core.InstrumentOption) core.Int64UpDownCounter {
	return recInstrument{name: name, m: m}
}

type recInstrument struct {
	name string
	m    *recMeter
}

func (i recInstrument) Add(_ context.Context, v int64, attrs ...core.Attribute) {
	i.m.record(i.name, float64(v), attrs)
}
func (i recInstrument) Record(_ context.Context, v float64, attrs ...core.Attribute) {
	i.m.record(i.name, v, attrs)
}

func attrOf(p recPoint, key string) string { return p.attrs[key] }

// TestRunnerEmitsTurnAndToolMetrics asserts the Runner emits the issue-1023
// metric set at turn end and per tool call: a turn counter (tagged by finish
// reason), a steps counter, a tokens counter split by direction, and a
// tool-call counter tagged by tool + status (ok here).
func TestRunnerEmitsTurnAndToolMetrics(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "lookup", "looks up", func(ctx context.Context, in struct {
		Key string `json:"key"`
	}) (string, error) {
		return "value-for-" + in.Key, nil
	})
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "lookup", Args: core.NewRawJSON(json.RawMessage(`{"key":"x"}`))}}},
		StubTurn{Text: "done"},
	)
	meter := &recMeter{}
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, MeterProvider: meter})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "get x"}}, nil); err != nil {
		t.Fatal(err)
	}

	if turns := meter.named("agent.turns"); len(turns) != 1 || turns[0].value != 1 || attrOf(turns[0], "agent.finish_reason") == "" {
		t.Fatalf("agent.turns = %+v", turns)
	}
	if dur := meter.named("agent.turn.duration"); len(dur) != 1 {
		t.Fatalf("agent.turn.duration points = %+v", dur)
	}
	if steps := meter.named("agent.steps"); len(steps) != 1 || steps[0].value != 2 {
		t.Fatalf("agent.steps = %+v (want one point, value 2)", steps)
	}
	tokens := meter.named("agent.tokens")
	dirs := map[string]bool{}
	for _, p := range tokens {
		dirs[attrOf(p, "direction")] = true
	}
	if !dirs["input"] || !dirs["output"] {
		t.Fatalf("agent.tokens missing a direction: %+v", tokens)
	}
	calls := meter.named("agent.tool.calls")
	if len(calls) != 1 || calls[0].value != 1 || attrOf(calls[0], "tool") != "lookup" || attrOf(calls[0], "status") != "ok" {
		t.Fatalf("agent.tool.calls = %+v", calls)
	}
	if td := meter.named("agent.tool.duration"); len(td) != 1 || attrOf(td[0], "tool") != "lookup" {
		t.Fatalf("agent.tool.duration = %+v", td)
	}
}

// TestRunnerToolMetricStatuses asserts the tool-call metric carries the right
// terminal status for the non-ok outcomes: a handler error becomes an IsError
// result (status "tool_error"), and an approval denial is "denied".
func TestRunnerToolMetricStatuses(t *testing.T) {
	t.Run("tool_error", func(t *testing.T) {
		src := NewFuncSource()
		AddFunc(src, "boom", "fails", func(ctx context.Context, in struct{}) (string, error) {
			return "", errors.New("kaboom")
		})
		stub := NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "boom", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
			StubTurn{Text: "ok"},
		)
		meter := &recMeter{}
		r, _ := NewRunner(RunnerConfig{Provider: stub, Tools: src, MeterProvider: meter})
		r.Run(context.Background(), nil, nil)
		calls := meter.named("agent.tool.calls")
		if len(calls) != 1 || attrOf(calls[0], "status") != "tool_error" {
			t.Fatalf("agent.tool.calls = %+v (want status tool_error)", calls)
		}
	})

	t.Run("denied", func(t *testing.T) {
		src := NewFuncSource()
		AddFunc(src, "act", "acts", func(ctx context.Context, in struct{}) (string, error) {
			return "did", nil
		})
		stub := NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "act", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
			StubTurn{Text: "ok"},
		)
		meter := &recMeter{}
		r, _ := NewRunner(RunnerConfig{
			Provider:      stub,
			Tools:         src,
			MeterProvider: meter,
			Approval:      denyAll{reason: "nope"},
		})
		r.Run(context.Background(), nil, nil)
		calls := meter.named("agent.tool.calls")
		if len(calls) != 1 || attrOf(calls[0], "status") != "denied" {
			t.Fatalf("agent.tool.calls = %+v (want status denied)", calls)
		}
	})
}
