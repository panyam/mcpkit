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

// TestRunnerInstructionsFuncPerTurn verifies InstructionsFunc overrides the
// static Instructions and is recomputed each turn (the dynamic system prompt
// that lets late-connecting servers' eager skills appear on the next turn).
func TestRunnerInstructionsFuncPerTurn(t *testing.T) {
	stub := NewStubProvider(StubTurn{Text: "a"}, StubTurn{Text: "b"})
	turn := 0
	r, err := NewRunner(RunnerConfig{
		Provider:     stub,
		Instructions: "static",
		InstructionsFunc: func(context.Context) string {
			turn++
			return fmt.Sprintf("dynamic-%d", turn)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Run(context.Background(), []Message{{Role: RoleUser, Text: "one"}}, nil)
	r.Run(context.Background(), []Message{{Role: RoleUser, Text: "two"}}, nil)

	reqs := stub.Requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	if reqs[0].Instructions != "dynamic-1" {
		t.Errorf("turn 1 instructions = %q, want dynamic-1 (func overrides static)", reqs[0].Instructions)
	}
	if reqs[1].Instructions != "dynamic-2" {
		t.Errorf("turn 2 instructions = %q, want dynamic-2 (recomputed per turn)", reqs[1].Instructions)
	}
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
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "lookup", Args: core.NewRawJSON(json.RawMessage(`{"key":"x"}`))}}},
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
			{ID: "c1", Name: "a", Args: core.NewRawJSON(json.RawMessage(`{}`))},
			{ID: "c2", Name: "b", Args: core.NewRawJSON(json.RawMessage(`{}`))},
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
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "no_such_tool", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
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
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "flaky", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
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
	loop := StubTurn{ToolCalls: []ToolCall{{ID: "c", Name: "spin", Args: core.NewRawJSON(json.RawMessage(`{}`))}}}
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
		{Kind: EventToolBegin, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: core.NewRawJSON(json.RawMessage(`{}`))}},
		{Kind: EventToolEnd, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: core.NewRawJSON(json.RawMessage(`{}`))}, ToolResult: &core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}},
		{Kind: EventToolError, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: core.NewRawJSON(json.RawMessage(`{}`))}, Error: "boom"},
		{Kind: EventToolDenied, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: core.NewRawJSON(json.RawMessage(`{}`))}, Reason: "declined by user"},
		{Kind: EventToolCancelled, Step: 1, ToolCall: &ToolCall{ID: "c", Name: "n", Args: core.NewRawJSON(json.RawMessage(`{}`))}, Reason: "cancelled by user"},
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

// denyAll is an ApprovalPolicy that refuses every call with a fixed reason.
type denyAll struct{ reason string }

func (d denyAll) Approve(context.Context, ApprovalRequest) (ApprovalDecision, error) {
	return ApprovalDecision{Reason: d.reason}, nil
}

func TestRunnerApprovalDeniedFeedsBackAndContinues(t *testing.T) {
	called := false
	src := NewFuncSource()
	AddFunc(src, "send_email", "sends mail", func(ctx context.Context, in struct {
		To string `json:"to"`
	}) (string, error) {
		called = true
		return "sent", nil
	})

	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "send_email", Args: core.NewRawJSON(json.RawMessage(`{"to":"a@b.c"}`))}}},
		StubTurn{Text: "I could not send it."},
	)
	emit, events := collectEvents()

	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src, Approval: denyAll{reason: "declined by user"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "email a@b.c"}}, emit)
	if err != nil {
		t.Fatal(err)
	}

	if called {
		t.Fatal("denied tool must not execute")
	}
	if res.Steps != 2 || res.Text != "I could not send it." {
		t.Fatalf("turn should continue past the denial: %+v", res)
	}

	// The denial surfaces as a distinct event and as model-visible tool text.
	want := []EventKind{EventTurnBegin, EventToolBegin, EventToolDenied, EventTextDelta, EventTurnEnd}
	if fmt.Sprint(kinds(*events)) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", kinds(*events), want)
	}
	var denied *Event
	for i := range *events {
		if (*events)[i].Kind == EventToolDenied {
			denied = &(*events)[i]
		}
	}
	if denied == nil || denied.Reason != "declined by user" || denied.ToolCall.Name != "send_email" {
		t.Fatalf("tool-denied event = %+v", denied)
	}
	toolMsg := stub.Requests()[1].Messages[2]
	if toolMsg.Role != RoleTool || !strings.Contains(toolMsg.Text, "not permitted") || !strings.Contains(toolMsg.Text, "declined by user") {
		t.Fatalf("model-visible denial text = %+v", toolMsg)
	}
}

func TestRunnerApprovalAllowedRunsTool(t *testing.T) {
	called := false
	src := NewFuncSource()
	AddFunc(src, "read_status", "reads status", func(ctx context.Context, _ struct{}) (string, error) {
		called = true
		return "green", nil
	})
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "read_status", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		StubTurn{Text: "Status is green."},
	)
	emit, events := collectEvents()

	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: src,
		Approval: NewTieredApproval(WithDefaultMode(ModeAlwaysAllow))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "status?"}}, emit); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("allowed tool should execute")
	}
	want := []EventKind{EventTurnBegin, EventToolBegin, EventToolEnd, EventTextDelta, EventTurnEnd}
	if fmt.Sprint(kinds(*events)) != fmt.Sprint(want) {
		t.Fatalf("allowed path should reach tool-end: %v", kinds(*events))
	}
}

func TestRunnerStructuredFinalizesAfterTerminalText(t *testing.T) {
	schema := core.NewRawJSON(json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`))
	stub := NewStubProvider(
		StubTurn{Text: "The answer is 42."}, // terminal text (Stream)
		StubTurn{Text: `{"answer":42}`},     // finalizing coercion (Generate)
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, ResponseSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "what is the answer"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "The answer is 42." {
		t.Fatalf("text answer should be unchanged: %q", res.Text)
	}
	var got struct {
		Answer int `json:"answer"`
	}
	if err := res.Structured.Bind(&got); err != nil || got.Answer != 42 {
		t.Fatalf("structured = %s (bind err %v)", res.Structured.Raw(), err)
	}
	// The finalizing Generate consumed a second turn.
	if len(stub.Requests()) != 2 {
		t.Fatalf("want 2 provider calls (stream + finalize), got %d", len(stub.Requests()))
	}
	// The finalizing request carries the schema and offers no tools.
	fin := stub.Requests()[1]
	if fin.ResponseSchema.Len() == 0 || len(fin.Tools) != 0 {
		t.Fatalf("finalize request should set schema and no tools: %+v", fin)
	}
}

func TestRunnerNoSchemaSkipsFinalize(t *testing.T) {
	stub := NewStubProvider(StubTurn{Text: "plain answer"})
	r, err := NewRunner(RunnerConfig{Provider: stub})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Structured.Len() != 0 {
		t.Fatalf("no schema means no structured output: %s", res.Structured.Raw())
	}
	if len(stub.Requests()) != 1 {
		t.Fatalf("no schema means no finalize call, got %d calls", len(stub.Requests()))
	}
}

func TestRunnerStructuredRetriesInvalidJSON(t *testing.T) {
	schema := core.NewRawJSON(json.RawMessage(`{"type":"object"}`))
	stub := NewStubProvider(
		StubTurn{Text: "done"},            // terminal text
		StubTurn{Text: "not json at all"}, // first finalize: invalid
		StubTurn{Text: `{"ok":true}`},     // retry: valid
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, ResponseSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Structured.Raw()) != `{"ok":true}` {
		t.Fatalf("retry should have produced valid JSON, got %s", res.Structured.Raw())
	}
	if len(stub.Requests()) != 3 {
		t.Fatalf("want stream + 2 finalize attempts = 3 calls, got %d", len(stub.Requests()))
	}
}

func TestRunnerStructuredProviderErrorAbortsTurn(t *testing.T) {
	schema := core.NewRawJSON(json.RawMessage(`{"type":"object"}`))
	stub := NewStubProvider(
		StubTurn{Text: "done"},                    // terminal text
		StubTurn{Err: errors.New("upstream 500")}, // finalize fails
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, ResponseSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil); err == nil {
		t.Fatal("a failed structured finalize must abort the turn")
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
