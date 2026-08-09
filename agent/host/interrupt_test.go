package host

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

// recordingObserver captures HostEvents and can run a hook on each one, so a
// test can act at an exact point in a turn rather than racing a sleep.
type recordingObserver struct {
	mu     sync.Mutex
	runner []agent.Event
	hook   func(agent.Event)
}

func (o *recordingObserver) On(e HostEvent) {
	if e.Kind != HostRunnerEvent || e.RunnerEvent.Kind == "" {
		return
	}
	o.mu.Lock()
	o.runner = append(o.runner, e.RunnerEvent)
	hook := o.hook
	o.mu.Unlock()
	if hook != nil {
		hook(e.RunnerEvent)
	}
}

func (o *recordingObserver) kinds() []agent.EventKind {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]agent.EventKind, 0, len(o.runner))
	for _, e := range o.runner {
		out = append(out, e.Kind)
	}
	return out
}

func has(kinds []agent.EventKind, want agent.EventKind) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// blockingToolServer serves a "block" tool that parks until release is closed
// or its context is cancelled, which is what makes a mid-call interrupt
// observable without a sleep-based race.
func blockingToolServer(t *testing.T, release <-chan struct{}) *httptest.Server {
	t.Helper()
	srv := testutil.NewTestServer()
	srv.Register(core.TextTool[struct{}]("block", "Blocks until released or cancelled",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			select {
			case <-release:
				return "released", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	))
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts
}

// TestInterruptToolCallsNoTurnRunning pins the between-turns guard. A control
// send with nothing draining would otherwise sit buffered and cancel the next
// turn's first tool call, which agent.TurnRequest.Control warns about
// explicitly, so RunTurn clears the channel and this must report false.
func TestInterruptToolCallsNoTurnRunning(t *testing.T) {
	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), &strings.Builder{}, strings.NewReader(""),
		WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "hi"})))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if app.InterruptToolCalls() {
		t.Fatal("InterruptToolCalls must report false with no turn running")
	}
	if err := app.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if app.InterruptToolCalls() {
		t.Fatal("InterruptToolCalls must report false again once the turn has ended")
	}
}

// TestInterruptToolCallsCancelsCallAndTurnContinues is the wiring acceptance:
// App.RunTurn must drive agent.Runner.RunTurn with a live control channel, so
// a surface can cancel one in-flight tool call and still get an answer. Before
// this wiring the host called Runner.Run, which takes no control channel, and
// the only interrupt available was cancelling ctx and losing the whole turn.
//
// The per-call cancellation semantics themselves are covered at the agent
// layer (TestRunTurnControlCancelsOneCallTurnContinues and friends); this
// asserts the host actually reaches them.
func TestInterruptToolCallsCancelsCallAndTurnContinues(t *testing.T) {
	release := make(chan struct{})
	defer close(release) // unblock the tool if the test fails before cancelling

	ts := blockingToolServer(t, release)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: "block", Args: core.NewRawJSON(json.RawMessage(`{}`)),
		}}},
		agent.StubTurn{Text: "the tool was cancelled, so here is an answer without it"},
	)

	obs := &recordingObserver{}
	interrupted := make(chan bool, 1)
	// Fire the interrupt the moment the tool starts, so the call is provably
	// in flight rather than probably in flight. app is assigned below and read
	// through the closure; the hook cannot run before RunTurn, which is well
	// after NewApp returns.
	var app *App
	obs.hook = func(e agent.Event) {
		if e.Kind == agent.EventToolBegin {
			interrupted <- app.InterruptToolCalls()
		}
	}

	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""),
		WithProvider(stub), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	done := make(chan error, 1)
	go func() { done <- app.RunTurn(context.Background(), "call the blocking tool") }()

	select {
	case ok := <-interrupted:
		if !ok {
			t.Fatal("InterruptToolCalls reported false while a tool call was in flight")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tool never started")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("turn must survive a cancelled tool call, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("turn did not finish after the interrupt; events so far: %v", obs.kinds())
	}

	kinds := obs.kinds()
	if !has(kinds, agent.EventToolCancelled) {
		t.Fatalf("expected EventToolCancelled, got %v", kinds)
	}

	// The model is told, and the turn ran on to a second step and answered.
	if n := len(stub.Requests()); n < 2 {
		t.Fatalf("turn stopped after the cancelled call: %d provider requests, want >= 2", n)
	}
	toolMsg := stub.Requests()[1].Messages[2]
	if toolMsg.Role != agent.RoleTool || !strings.Contains(strings.ToLower(toolMsg.Text), "cancel") {
		t.Fatalf("model-visible cancellation missing: %+v", toolMsg)
	}
}
