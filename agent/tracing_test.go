package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

type fakeSpan struct {
	name   string
	parent string
	attrs  map[string]string
	errs   []error
	ended  bool
	mu     sync.Mutex
}

func (s *fakeSpan) End() { s.mu.Lock(); s.ended = true; s.mu.Unlock() }
func (s *fakeSpan) SetAttribute(k, v string) {
	s.mu.Lock()
	s.attrs[k] = v
	s.mu.Unlock()
}
func (s *fakeSpan) RecordError(err error) { s.mu.Lock(); s.errs = append(s.errs, err); s.mu.Unlock() }
func (s *fakeSpan) AddLink(core.Link)     {}

type fakeTPKey struct{}

type fakeTP struct {
	mu    sync.Mutex
	spans []*fakeSpan
}

func (tp *fakeTP) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	sp := &fakeSpan{name: name, attrs: map[string]string{}}
	for _, a := range attrs {
		sp.attrs[a.Key] = a.Value
	}
	if parent, ok := ctx.Value(fakeTPKey{}).(*fakeSpan); ok {
		sp.parent = parent.name
	}
	tp.mu.Lock()
	tp.spans = append(tp.spans, sp)
	tp.mu.Unlock()
	return context.WithValue(ctx, fakeTPKey{}, sp), sp
}

func (tp *fakeTP) byName(name string) []*fakeSpan {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	var out []*fakeSpan
	for _, s := range tp.spans {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

func TestRunnerSpanTree(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "lookup", "", func(ctx context.Context, _ struct{}) (string, error) { return "v", nil })
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "lookup", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		StubTurn{Deltas: []Delta{
			{Kind: DeltaText, Text: "done"},
			{Kind: DeltaFinish, FinishReason: "stop"},
			{Kind: DeltaUsage, Usage: &Usage{InputTokens: 9, OutputTokens: 4}},
		}},
	)
	tp := &fakeTP{}
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, TracerProvider: tp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}

	turns := tp.byName("agent.turn")
	steps := tp.byName("agent.step")
	tools := tp.byName("agent.tool")
	if len(turns) != 1 || len(steps) != 2 || len(tools) != 1 {
		t.Fatalf("span counts: turn=%d step=%d tool=%d", len(turns), len(steps), len(tools))
	}
	turn := turns[0]
	if !turn.ended || turn.attrs["agent.steps"] != "2" || turn.attrs["agent.finish_reason"] != "stop" ||
		turn.attrs["agent.tokens.input"] != "9" || turn.attrs["agent.tokens.output"] != "4" {
		t.Fatalf("turn span = %+v", turn.attrs)
	}
	for i, s := range steps {
		if s.parent != "agent.turn" || !s.ended {
			t.Fatalf("step %d parenting: %+v", i, s)
		}
		if s.attrs["agent.step"] != fmt.Sprint(i+1) {
			t.Fatalf("step attr = %+v", s.attrs)
		}
	}
	tool := tools[0]
	if tool.parent != "agent.step" || tool.attrs["agent.tool.name"] != "lookup" || tool.attrs["agent.tool.is_error"] != "false" || !tool.ended {
		t.Fatalf("tool span = %+v parent=%s", tool.attrs, tool.parent)
	}
}

func TestRunnerSpanRecordsDispatchAndTurnErrors(t *testing.T) {
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "ghost", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
	)
	tp := &fakeTP{}
	r, _ := NewRunner(RunnerConfig{Provider: stub, Tools: NewFuncSource(), TracerProvider: tp, MaxSteps: 1})

	_, err := r.Run(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("want exhaustion/step-cap error")
	}
	tools := tp.byName("agent.tool")
	if len(tools) != 1 || len(tools[0].errs) == 0 || !errors.Is(tools[0].errs[0], ErrUnknownTool) {
		t.Fatalf("tool span must record the dispatch error: %+v", tools[0])
	}
	turns := tp.byName("agent.turn")
	if len(turns) != 1 || len(turns[0].errs) == 0 || !turns[0].ended {
		t.Fatalf("turn span must record the abort and end: %+v", turns[0])
	}
}

func TestRunnerNoopTracerByDefault(t *testing.T) {
	stub := NewStubProvider(StubTurn{Text: "hi"})
	r, err := NewRunner(RunnerConfig{Provider: stub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatalf("nil TracerProvider must default to noop: %v", err)
	}
}
