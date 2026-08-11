package agent

import (
	"context"
	"strings"
	"testing"
)

// cancelHarness wires a runner over a blocking "slow" tool and an
// immediate "fast" tool, so tests can steer per-call cancellation
// deterministically: slow only returns when its ctx is cancelled.
func cancelHarness(t *testing.T, turns ...StubTurn) (*Runner, *FuncSource) {
	t.Helper()
	src := NewFuncSource()
	if err := AddFunc(src, "slow", "blocks until cancelled", func(ctx context.Context, in struct{}) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := AddFunc(src, "fast", "returns immediately", func(ctx context.Context, in struct{}) (string, error) {
		return "fast done", nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{Provider: NewStubProvider(turns...), Tools: src})
	if err != nil {
		t.Fatal(err)
	}
	return r, src
}

func toolMessage(t *testing.T, msgs []Message, callID string) Message {
	t.Helper()
	for _, m := range msgs {
		if m.Role == RoleTool && m.ToolCallID == callID {
			return m
		}
	}
	t.Fatalf("no RoleTool message for call %q in %+v", callID, msgs)
	return Message{}
}

func TestRunTurnControlCancelsOneCallTurnContinues(t *testing.T) {
	r, _ := cancelHarness(t,
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "slow"}, {ID: "c2", Name: "fast"}}},
		StubTurn{Text: "wrapped up"},
	)

	control := make(chan Control, 1)
	var events []Event
	emit := func(e Event) {
		events = append(events, e)
		if e.Kind == EventToolBegin && e.ToolCall.ID == "c1" {
			control <- Control{CallID: "c1"}
		}
	}
	res, err := r.RunTurn(context.Background(), TurnRequest{
		History: []Message{{Role: RoleUser, Text: "go"}},
		Emit:    emit,
		Control: control,
	})
	if err != nil {
		t.Fatalf("RunTurn: %v (a per-call cancel must not abort the turn)", err)
	}
	if res.Text != "wrapped up" || res.Steps != 2 {
		t.Fatalf("turn did not continue past the cancel: %+v", res)
	}
	if got := toolMessage(t, res.Messages, "c1").Text; got != "cancelled by user" {
		t.Fatalf("cancelled call fed back %q, want \"cancelled by user\"", got)
	}
	if got := toolMessage(t, res.Messages, "c2").Text; got != "fast done" {
		t.Fatalf("sibling call was disturbed: %q", got)
	}

	cancelledEvents, errorEvents := 0, 0
	for _, e := range events {
		if e.ToolCall == nil || e.ToolCall.ID != "c1" {
			continue
		}
		switch e.Kind {
		case EventToolCancelled:
			cancelledEvents++
			if e.Reason != "cancelled by user" {
				t.Fatalf("tool-cancelled Reason = %q", e.Reason)
			}
		case EventToolError, EventToolEnd:
			errorEvents++
		}
	}
	if cancelledEvents != 1 || errorEvents != 0 {
		t.Fatalf("cancelled call emitted %d tool-cancelled and %d tool-error/tool-end events, want 1 and 0", cancelledEvents, errorEvents)
	}
}

func TestRunTurnControlEmptyIDCancelsAllInFlight(t *testing.T) {
	r, _ := cancelHarness(t,
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "slow"}, {ID: "c2", Name: "slow"}}},
		StubTurn{Text: "after cancel-all"},
	)

	control := make(chan Control, 1)
	begins := 0
	emit := func(e Event) {
		if e.Kind == EventToolBegin {
			begins++
			if begins == 2 {
				control <- Control{}
			}
		}
	}
	res, err := r.RunTurn(context.Background(), TurnRequest{
		History: []Message{{Role: RoleUser, Text: "go"}},
		Emit:    emit,
		Control: control,
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	for _, id := range []string{"c1", "c2"} {
		if got := toolMessage(t, res.Messages, id).Text; got != "cancelled by user" {
			t.Fatalf("call %s fed back %q, want \"cancelled by user\"", id, got)
		}
	}
	if res.Text != "after cancel-all" {
		t.Fatalf("turn did not continue: %+v", res)
	}
}

func TestRunTurnControlUnknownCallIDIsNoOp(t *testing.T) {
	r, _ := cancelHarness(t,
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "fast"}}},
		StubTurn{Text: "fine"},
	)

	control := make(chan Control, 1)
	emit := func(e Event) {
		if e.Kind == EventToolBegin {
			control <- Control{CallID: "never-dispatched"}
		}
	}
	res, err := r.RunTurn(context.Background(), TurnRequest{
		History: []Message{{Role: RoleUser, Text: "go"}},
		Emit:    emit,
		Control: control,
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if got := toolMessage(t, res.Messages, "c1").Text; got != "fast done" {
		t.Fatalf("stray Control disturbed an unrelated call: %q", got)
	}
}

// TestRunTurnCtxCancelStillAbortsTurn pins the coarse-grained behavior:
// cancelling RunTurn's own ctx aborts the whole turn (per-call Controls
// are the only continue-after-cancel path).
func TestRunTurnCtxCancelStillAbortsTurn(t *testing.T) {
	r, _ := cancelHarness(t,
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "slow"}}},
		StubTurn{Text: "unreachable"},
	)

	ctx, cancel := context.WithCancel(context.Background())
	emit := func(e Event) {
		if e.Kind == EventToolBegin {
			cancel()
		}
	}
	_, err := r.RunTurn(ctx, TurnRequest{
		History: []Message{{Role: RoleUser, Text: "go"}},
		Emit:    emit,
		Control: make(chan Control),
	})
	if err == nil {
		t.Fatal("RunTurn survived its own ctx being cancelled")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("unexpected abort error: %v", err)
	}
}

// TestRunDelegatesToRunTurn pins that the legacy signature is a pure
// shim: same result shape, no Control plumbing required.
func TestRunDelegatesToRunTurn(t *testing.T) {
	r, _ := cancelHarness(t, StubTurn{Text: "plain"})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "plain" || len(res.Messages) != 1 {
		t.Fatalf("Run via RunTurn returned %+v", res)
	}
}
