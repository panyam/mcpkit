package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
)

func collectEvents() (func(Event), *[]Event) {
	var events []Event
	return func(e Event) { events = append(events, e) }, &events
}

func kinds(events []Event) []EventKind {
	out := make([]EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

func newRunner(t *testing.T, p Provider, src ToolSource) *Runner {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: p, Tools: src, Instructions: "be helpful"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunnerSingleStepText(t *testing.T) {
	stub := NewStubProvider(StubTurn{Deltas: []Delta{
		{Kind: DeltaText, Text: "Hello "},
		{Kind: DeltaText, Text: "there"},
		{Kind: DeltaFinish, FinishReason: "stop"},
	}})
	emit, events := collectEvents()

	res, err := newRunner(t, stub, nil).Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello there" || res.Steps != 1 || res.FinishReason != "stop" {
		t.Fatalf("result = %+v", res)
	}
	want := []EventKind{EventTurnBegin, EventTextDelta, EventTextDelta, EventTurnEnd}
	if fmt.Sprint(kinds(*events)) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", kinds(*events), want)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != RoleAssistant {
		t.Fatalf("messages = %+v", res.Messages)
	}
	if reqs := stub.Requests(); len(reqs[0].Tools) != 0 || reqs[0].Instructions != "be helpful" {
		t.Fatalf("request = %+v", reqs[0])
	}
}

func TestRunnerMultiStepToolLoopThreadsHistory(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "lookup", "looks up", func(ctx context.Context, in struct {
		Key string `json:"key"`
	}) (string, error) {
		return "value-for-" + in.Key, nil
	})

	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "lookup", Args: json.RawMessage(`{"key":"x"}`)}}},
		StubTurn{Text: "The value is value-for-x."},
	)
	emit, events := collectEvents()

	res, err := newRunner(t, stub, src).Run(context.Background(), []Message{{Role: RoleUser, Text: "get x"}}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 2 || res.Text != "The value is value-for-x." {
		t.Fatalf("result = %+v", res)
	}

	want := []EventKind{EventTurnBegin, EventToolBegin, EventToolEnd, EventTextDelta, EventTurnEnd}
	if fmt.Sprint(kinds(*events)) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", kinds(*events), want)
	}

	reqs := stub.Requests()
	second := reqs[1].Messages
	if len(second) != 3 {
		t.Fatalf("second request messages = %+v", second)
	}
	if second[1].Role != RoleAssistant || len(second[1].ToolCalls) != 1 {
		t.Fatalf("assistant threading = %+v", second[1])
	}
	if second[2].Role != RoleTool || second[2].ToolCallID != "c1" || second[2].Text != "value-for-x" {
		t.Fatalf("tool threading = %+v", second[2])
	}
	if len(res.Messages) != 3 {
		t.Fatalf("added messages = %d, want 3", len(res.Messages))
	}
	if reqs[1].Tools[0].Name != "lookup" {
		t.Fatalf("tools must be re-listed per step, got %+v", reqs[1].Tools)
	}
}

func TestRunnerParallelToolCallsRendezvous(t *testing.T) {
	first := make(chan struct{})
	second := make(chan struct{})
	src := NewFuncSource()
	AddFunc(src, "a", "", func(ctx context.Context, _ struct{}) (string, error) {
		close(first)
		select {
		case <-second:
			return "a-done", nil
		case <-time.After(2 * time.Second):
			return "", errors.New("tool a never saw tool b start: dispatch is serial")
		}
	})
	AddFunc(src, "b", "", func(ctx context.Context, _ struct{}) (string, error) {
		close(second)
		select {
		case <-first:
			return "b-done", nil
		case <-time.After(2 * time.Second):
			return "", errors.New("tool b never saw tool a start: dispatch is serial")
		}
	})

	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{
			{ID: "c1", Name: "a", Args: json.RawMessage(`{}`)},
			{ID: "c2", Name: "b", Args: json.RawMessage(`{}`)},
		}},
		StubTurn{Text: "both done"},
	)

	res, err := newRunner(t, stub, src).Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolMsgs := res.Messages[1:3]
	if toolMsgs[0].ToolCallID != "c1" || toolMsgs[0].Text != "a-done" {
		t.Fatalf("call-order threading broken: %+v", toolMsgs)
	}
	if toolMsgs[1].ToolCallID != "c2" || toolMsgs[1].Text != "b-done" {
		t.Fatalf("call-order threading broken: %+v", toolMsgs)
	}
}

func TestRunnerDispatchErrorFedToModel(t *testing.T) {
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "no_such_tool", Args: json.RawMessage(`{}`)}}},
		StubTurn{Text: "recovered"},
	)
	emit, events := collectEvents()

	res, err := newRunner(t, stub, NewFuncSource()).Run(context.Background(), nil, emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "recovered" {
		t.Fatalf("loop must continue past dispatch errors, got %+v", res)
	}
	var sawToolError bool
	for _, e := range *events {
		if e.Kind == EventToolError && strings.Contains(e.Error, "no_such_tool") {
			sawToolError = true
		}
		if e.Kind == EventToolEnd {
			t.Fatalf("dispatch failure must not emit tool-end: %+v", e)
		}
	}
	if !sawToolError {
		t.Fatalf("missing tool-error event: %v", kinds(*events))
	}
	toolMsg := stub.Requests()[1].Messages[1]
	if toolMsg.Role != RoleTool || !strings.Contains(toolMsg.Text, "tool call failed") {
		t.Fatalf("model must see the failure: %+v", toolMsg)
	}
}

func TestRunnerIsErrorResultIsToolEndNotToolError(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "flaky", "", func(ctx context.Context, _ struct{}) (string, error) {
		return "", errors.New("kaput")
	})
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "flaky", Args: json.RawMessage(`{}`)}}},
		StubTurn{Text: "noted"},
	)
	emit, events := collectEvents()

	if _, err := newRunner(t, stub, src).Run(context.Background(), nil, emit); err != nil {
		t.Fatal(err)
	}
	var sawEnd bool
	for _, e := range *events {
		if e.Kind == EventToolError {
			t.Fatalf("IsError result must be tool-end, got tool-error: %+v", e)
		}
		if e.Kind == EventToolEnd {
			sawEnd = true
			if e.ToolResult == nil || !e.ToolResult.IsError {
				t.Fatalf("tool-end must carry the IsError result: %+v", e)
			}
		}
	}
	if !sawEnd {
		t.Fatal("missing tool-end event")
	}
	toolMsg := stub.Requests()[1].Messages[1]
	if !strings.Contains(toolMsg.Text, "tool reported an error") || !strings.Contains(toolMsg.Text, "kaput") {
		t.Fatalf("model must see the error marker: %+v", toolMsg)
	}
}

func TestRunnerStepCap(t *testing.T) {
	loop := StubTurn{ToolCalls: []ToolCall{{ID: "c", Name: "spin", Args: json.RawMessage(`{}`)}}}
	stub := NewStubProvider(loop, loop, loop)
	src := NewFuncSource()
	AddFunc(src, "spin", "", func(ctx context.Context, _ struct{}) (string, error) { return "again", nil })

	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	emit, events := collectEvents()
	_, err = r.Run(context.Background(), nil, emit)
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("want ErrMaxSteps, got %v", err)
	}
	last := (*events)[len(*events)-1]
	if last.Kind != EventError || !strings.Contains(last.Error, "max steps") {
		t.Fatalf("last event = %+v", last)
	}
}

type blockingStream struct{ ctx context.Context }

func (b *blockingStream) Recv() (Delta, error) {
	<-b.ctx.Done()
	return Delta{}, b.ctx.Err()
}
func (b *blockingStream) Close() error { return nil }

type blockingProvider struct{}

func (blockingProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	return &blockingStream{ctx: ctx}, nil
}
func (blockingProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	return nil, errors.New("unused")
}

func TestRunnerCancellationAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	emit, events := collectEvents()
	_, err := newRunner(t, blockingProvider{}, nil).Run(ctx, nil, emit)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	last := (*events)[len(*events)-1]
	if last.Kind != EventError {
		t.Fatalf("last event = %+v", last)
	}
}

func TestRunnerThinkingMarkers(t *testing.T) {
	stub := NewStubProvider(StubTurn{Deltas: []Delta{
		{Kind: DeltaReasoning, Text: "hmm "},
		{Kind: DeltaReasoning, Text: "ok"},
		{Kind: DeltaText, Text: "answer"},
		{Kind: DeltaFinish, FinishReason: "stop"},
	}})
	emit, events := collectEvents()
	if _, err := newRunner(t, stub, nil).Run(context.Background(), nil, emit); err != nil {
		t.Fatal(err)
	}
	want := []EventKind{EventTurnBegin, EventThinkingBegin, EventThinkingDelta, EventThinkingDelta, EventThinkingEnd, EventTextDelta, EventTurnEnd}
	if fmt.Sprint(kinds(*events)) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", kinds(*events), want)
	}
}

func TestRunnerUsageAggregation(t *testing.T) {
	src := NewFuncSource()
	AddFunc(src, "t", "", func(ctx context.Context, _ struct{}) (string, error) { return "ok", nil })
	stub := NewStubProvider(
		StubTurn{Deltas: []Delta{
			{Kind: DeltaToolCallStart, Index: 0, ToolCallID: "c1", ToolName: "t", Text: "{}"},
			{Kind: DeltaFinish, FinishReason: "tool_calls"},
			{Kind: DeltaUsage, Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
		}},
		StubTurn{Deltas: []Delta{
			{Kind: DeltaText, Text: "done"},
			{Kind: DeltaFinish, FinishReason: "stop"},
			{Kind: DeltaUsage, Usage: &Usage{InputTokens: 20, OutputTokens: 7}},
		}},
	)
	res, err := newRunner(t, stub, src).Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.InputTokens != 30 || res.Usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestRunnerEventKindsJSONRoundTrip(t *testing.T) {
	events := []Event{
		{Kind: EventTurnBegin},
		{Kind: EventThinkingBegin, Step: 1},
		{Kind: EventThinkingDelta, Step: 1, Text: "hmm"},
		{Kind: EventThinkingEnd, Step: 1},
		{Kind: EventTextDelta, Step: 1, Text: "hi"},
		{Kind: EventToolBegin, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: json.RawMessage(`{}`)}},
		{Kind: EventToolEnd, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: json.RawMessage(`{}`)}, ToolResult: &core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}},
		{Kind: EventToolError, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: json.RawMessage(`{}`)}, Error: "boom"},
		{Kind: EventTurnEnd, Result: &TurnResult{Text: "done", Steps: 1, Usage: Usage{InputTokens: 1, OutputTokens: 2}}},
		{Kind: EventError, Error: "fatal"},
	}
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal %s: %v", e.Kind, err)
		}
		var back Event
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", e.Kind, err)
		}
		raw2, err := json.Marshal(back)
		if err != nil {
			t.Fatalf("re-marshal %s: %v", e.Kind, err)
		}
		if string(raw) != string(raw2) {
			t.Fatalf("round-trip drift for %s: %s vs %s", e.Kind, raw, raw2)
		}
	}
}

func TestRunnerRequiresProvider(t *testing.T) {
	if _, err := NewRunner(RunnerConfig{}); err == nil {
		t.Fatal("want error for missing provider")
	}
}

func TestToolResultTextFallbacks(t *testing.T) {
	multi := &core.ToolResult{Content: []core.Content{{Type: "text", Text: "a"}, {Type: "text", Text: "b"}}}
	if got := toolResultText(multi); got != "a\nb" {
		t.Fatalf("multi-text = %q", got)
	}
	structured := &core.ToolResult{StructuredContent: map[string]any{"n": 1}}
	if got := toolResultText(structured); got != `{"n":1}` {
		t.Fatalf("structured = %q", got)
	}
	empty := &core.ToolResult{}
	if got := toolResultText(empty); got != "(empty result)" {
		t.Fatalf("empty = %q", got)
	}
}
