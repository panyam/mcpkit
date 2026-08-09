package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// echoHarness gives a Runner one tool that reports the argument it received,
// so a test can prove what actually executed rather than what was requested.
func echoHarness(t *testing.T, mw []ToolMiddleware, turns ...StubTurn) (*Runner, *[]string) {
	t.Helper()
	var seen []string
	src := NewFuncSource()
	type in struct {
		Msg string `json:"msg"`
	}
	if err := AddFunc(src, "echo", "echoes msg", func(_ context.Context, i in) (string, error) {
		seen = append(seen, i.Msg)
		return "echo: " + i.Msg, nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{Provider: NewStubProvider(turns...), Tools: src, ToolMiddleware: mw})
	if err != nil {
		t.Fatal(err)
	}
	return r, &seen
}

func echoCall(msg string) StubTurn {
	return StubTurn{ToolCalls: []ToolCall{{
		ID: "c1", Name: "echo",
		Args: core.NewRawJSON(json.RawMessage(`{"msg":"` + msg + `"}`)),
	}}}
}

func argMsg(t *testing.T, args core.RawJSON) string {
	t.Helper()
	var v struct {
		Msg string `json:"msg"`
	}
	if err := args.Bind(&v); err != nil {
		t.Fatalf("bind args: %v", err)
	}
	return v.Msg
}

// TestToolMiddlewareRewritesArgs pins that an argument rewrite reaches the
// tool. Redaction is the motivating case: a middleware that strips a secret
// is worthless if the original arguments execute anyway.
func TestToolMiddlewareRewritesArgs(t *testing.T) {
	redact := BeforeToolCall(func(_ context.Context, info *ToolCallInfo) error {
		info.Call.Args = core.NewRawJSON(json.RawMessage(`{"msg":"[redacted]"}`))
		return nil
	})
	r, seen := echoHarness(t, []ToolMiddleware{redact}, echoCall("sk-secret"), StubTurn{Text: "done"})

	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0] != "[redacted]" {
		t.Fatalf("tool saw %v, want the rewritten argument", *seen)
	}
}

// TestToolMiddlewareOrderIsOutermostFirst pins the composition rule: entry 0
// wraps entry 1, so the second sees the first's rewrite. A gate registered
// last depends on this to inspect what will really execute.
func TestToolMiddlewareOrderIsOutermostFirst(t *testing.T) {
	first := BeforeToolCall(func(_ context.Context, info *ToolCallInfo) error {
		info.Call.Args = core.NewRawJSON(json.RawMessage(`{"msg":"rewritten"}`))
		return nil
	})
	var sawSecond string
	second := BeforeToolCall(func(_ context.Context, info *ToolCallInfo) error {
		sawSecond = argMsg(t, info.Call.Args)
		return nil
	})
	r, _ := echoHarness(t, []ToolMiddleware{first, second}, echoCall("original"), StubTurn{Text: "done"})

	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if sawSecond != "rewritten" {
		t.Fatalf("second middleware saw %q, want the first's rewrite", sawSecond)
	}
}

// TestToolMiddlewareDenyStopsTheChain pins that a middleware which declines
// to call next ends the call: nothing further down runs and the tool never
// executes. That is what keeps a gate meaningful, since no middleware behind
// it can rewrite arguments it has already passed on.
func TestToolMiddlewareDenyStopsTheChain(t *testing.T) {
	deny := ToolMiddleware(func(_ context.Context, _ ToolCallInfo, _ ToolCallFunc) (*core.ToolResult, error) {
		return nil, DenyTool("nope")
	})
	innerRan := false
	inner := BeforeToolCall(func(_ context.Context, _ *ToolCallInfo) error {
		innerRan = true
		return nil
	})
	r, seen := echoHarness(t, []ToolMiddleware{deny, inner}, echoCall("hi"), StubTurn{Text: "ok"})

	var events []Event
	res, err := r.Run(context.Background(), nil, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("a denial must not abort the turn: %v", err)
	}
	if innerRan {
		t.Fatal("middleware inside the denier ran; declining to call next must stop the chain")
	}
	if len(*seen) != 0 {
		t.Fatalf("denied call still executed: %v", *seen)
	}

	var denied int
	for _, e := range events {
		switch e.Kind {
		case EventToolDenied:
			denied++
			if e.Reason != "nope" {
				t.Fatalf("tool-denied Reason = %q", e.Reason)
			}
		case EventToolEnd, EventToolError:
			t.Fatalf("a denied call must not emit %s", e.Kind)
		}
	}
	if denied != 1 {
		t.Fatalf("got %d tool-denied events, want 1", denied)
	}
	if got := toolMessage(t, res.Messages, "c1").Text; !strings.Contains(got, "nope") {
		t.Fatalf("model-visible denial = %q", got)
	}
}

// TestToolMiddlewareRewritesResult pins the result path, which is where
// marking untrusted tool output lands (#1058) alongside redaction.
func TestToolMiddlewareRewritesResult(t *testing.T) {
	mark := AfterToolCall(func(_ context.Context, _ ToolCallInfo, res *core.ToolResult) (*core.ToolResult, error) {
		return &core.ToolResult{Content: []core.Content{{
			Type: "text", Text: "<untrusted>" + toolResultText(res) + "</untrusted>",
		}}}, nil
	})
	r, _ := echoHarness(t, []ToolMiddleware{mark}, echoCall("hi"), StubTurn{Text: "done"})

	res, err := r.Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolMessage(t, res.Messages, "c1").Text; got != "<untrusted>echo: hi</untrusted>" {
		t.Fatalf("model saw %q, want the rewritten result", got)
	}
}

// TestToolMiddlewareCanServeWithoutCalling is what the wrapping shape buys
// over a flat before/after pair: a middleware can answer from a cache and
// never dispatch. A phase-based seam cannot express this.
func TestToolMiddlewareCanServeWithoutCalling(t *testing.T) {
	cached := ToolMiddleware(func(_ context.Context, _ ToolCallInfo, _ ToolCallFunc) (*core.ToolResult, error) {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "from cache"}}}, nil
	})
	r, seen := echoHarness(t, []ToolMiddleware{cached}, echoCall("hi"), StubTurn{Text: "done"})

	res, err := r.Run(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 0 {
		t.Fatalf("tool ran despite a cache hit: %v", *seen)
	}
	if got := toolMessage(t, res.Messages, "c1").Text; got != "from cache" {
		t.Fatalf("model saw %q, want the cached result", got)
	}
}

// TestAfterToolCallSkippedOnDeny pins that a result transformer does not run
// for a call that produced no result, so it never has to nil-check.
func TestAfterToolCallSkippedOnDeny(t *testing.T) {
	afterRan := false
	after := AfterToolCall(func(_ context.Context, _ ToolCallInfo, res *core.ToolResult) (*core.ToolResult, error) {
		afterRan = true
		return nil, nil
	})
	deny := ToolMiddleware(func(_ context.Context, _ ToolCallInfo, _ ToolCallFunc) (*core.ToolResult, error) {
		return nil, DenyTool("no")
	})
	// after is outermost, so it wraps the denier and would see its error.
	r, _ := echoHarness(t, []ToolMiddleware{after, deny}, echoCall("hi"), StubTurn{Text: "ok"})

	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if afterRan {
		t.Fatal("AfterToolCall ran for a denied call")
	}
}

// TestTieredApprovalIsToolMiddleware is the collapse acceptance: permission
// is not a parallel mechanism, it is one middleware among others. A gate
// placed last must decide on the arguments an earlier entry rewrote, which is
// the whole reason agent/host appends it last.
func TestTieredApprovalIsToolMiddleware(t *testing.T) {
	var asked string
	gate := NewTieredApproval(WithAsk(func(_ context.Context, info ToolCallInfo) (bool, error) {
		asked = argMsg(t, info.Call.Args)
		return false, nil // refuse, so the call must not run
	}))
	rewrite := BeforeToolCall(func(_ context.Context, info *ToolCallInfo) error {
		info.Call.Args = core.NewRawJSON(json.RawMessage(`{"msg":"final"}`))
		return nil
	})

	r, seen := echoHarness(t, []ToolMiddleware{rewrite, gate.WrapToolCall},
		echoCall("original"), StubTurn{Text: "ok"})
	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if asked != "final" {
		t.Fatalf("gate was asked about %q, want the rewritten argument it would actually run", asked)
	}
	if len(*seen) != 0 {
		t.Fatalf("refused call executed anyway: %v", *seen)
	}
}
