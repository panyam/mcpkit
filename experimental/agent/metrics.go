package agent

import (
	"context"
	"time"

	"github.com/panyam/mcpkit/core"
)

// runnerMetrics holds the Runner's OTel instruments (issue 1023), the metrics
// sibling of the SEP-414 trace spans. They are emitted at the same points the
// spans are (turn end, each tool call), so a dashboard can chart rate, latency,
// token throughput, and tool error/deny rate. Built once from the configured
// MeterProvider; a NoopMeterProvider returns shared no-op instruments, so the
// unconfigured path is a branch-free zero-overhead call.
type runnerMetrics struct {
	turns     core.Int64Counter
	turnDur   core.Float64Histogram
	steps     core.Int64Counter
	tokens    core.Int64Counter
	toolCalls core.Int64Counter
	toolDur   core.Float64Histogram
}

func newRunnerMetrics(mp core.MeterProvider) *runnerMetrics {
	return &runnerMetrics{
		turns:     mp.Int64Counter("agent.turns", core.WithDescription("completed agent turns")),
		turnDur:   mp.Float64Histogram("agent.turn.duration", core.WithUnit("s"), core.WithDescription("turn wall-clock duration")),
		steps:     mp.Int64Counter("agent.steps", core.WithDescription("model steps (round-trips) across turns")),
		tokens:    mp.Int64Counter("agent.tokens", core.WithDescription("tokens consumed, by direction")),
		toolCalls: mp.Int64Counter("agent.tool.calls", core.WithDescription("tool calls, by tool and status")),
		toolDur:   mp.Float64Histogram("agent.tool.duration", core.WithUnit("s"), core.WithDescription("tool-call wall-clock duration")),
	}
}

// turnDone records the per-turn metrics: one turn, its duration, the steps it
// took, and the input/output tokens it consumed.
func (m *runnerMetrics) turnDone(ctx context.Context, steps int, in, out int, finishReason string, elapsed time.Duration) {
	m.turns.Add(ctx, 1, core.Attribute{Key: "agent.finish_reason", Value: finishReason})
	m.turnDur.Record(ctx, elapsed.Seconds())
	m.steps.Add(ctx, int64(steps))
	m.tokens.Add(ctx, int64(in), core.Attribute{Key: "direction", Value: "input"})
	m.tokens.Add(ctx, int64(out), core.Attribute{Key: "direction", Value: "output"})
}

// toolDone records one tool call with its tool name, terminal status
// (ok / error / denied / cancelled / unavailable / tool_error), and duration.
func (m *runnerMetrics) toolDone(ctx context.Context, tool, status string, elapsed time.Duration) {
	attrs := []core.Attribute{{Key: "tool", Value: tool}, {Key: "status", Value: status}}
	m.toolCalls.Add(ctx, 1, attrs...)
	m.toolDur.Record(ctx, elapsed.Seconds(), core.Attribute{Key: "tool", Value: tool})
}
