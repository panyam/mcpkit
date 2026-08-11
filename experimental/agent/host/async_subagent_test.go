package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

// waitFor polls until cond is true or the deadline passes.
func waitForCond(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

// TestAppAsyncSubAgentAcksThenInjects is the 1035 payoff: an Async persona acks
// immediately on the spawning turn, runs in the background, and its result is
// injected into a LATER turn's context (not this turn).
func TestAppAsyncSubAgentAcksThenInjects(t *testing.T) {
	ts := startTestServer(t)

	// main turn 1: delegate to the async researcher, then reply (the ack is the
	// tool result, so the main agent continues). The researcher (same stub)
	// answers when it runs in the background. main turn 2: a plain reply that
	// should now see the injected result.
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "a1", Name: "researcher",
			Args: core.NewRawJSON(json.RawMessage(`{"task":"deep dive on caching"}`)),
		}}},
		agent.StubTurn{Text: "kicked off the research"},  // main finishes turn 1
		agent.StubTurn{Text: "the deep research result"}, // the async child's answer
		agent.StubTurn{Text: "here is what came back"},   // main turn 2
	)

	cfg := testConfig(ts.URL)
	cfg.SubAgents = []SubAgentConfig{{
		Name: "researcher", Description: "long-running deep research", Instructions: "research deeply", Async: true,
	}}
	obs := &collectObserver{}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()

	// turn 1: the main agent delegates; the tool result is an ACK, and the turn
	// completes without waiting for the child.
	if err := app.RunTurn(ctx, "kick off deep research"); err != nil {
		t.Fatal(err)
	}
	ackFed := toolMsgByID(t, stub.Requests()[1].Messages, "a1").Text
	if !strings.Contains(ackFed, "started") {
		t.Fatalf("async delegate should feed the model an ack, got %q", ackFed)
	}

	// the background child finishes and notifies (HostMessage) — wait for it.
	waitForCond(t, 2*time.Second, func() bool { return len(obs.kinds(HostMessage)) > 0 })

	// turn 2: the injected result is now in the request's system context.
	if err := app.RunTurn(ctx, "anything back yet?"); err != nil {
		t.Fatal(err)
	}
	if !hasSystemContaining(stub.Requests()[3].Messages, "subagent.completed") ||
		!hasSystemContaining(stub.Requests()[3].Messages, "the deep research result") {
		t.Fatalf("async result not injected into the later turn: %+v", stub.Requests()[3].Messages)
	}
	// and it was NOT in turn 1's own request (it hadn't finished yet)
	if hasSystemContaining(stub.Requests()[0].Messages, "subagent.completed") {
		t.Fatal("async result must not appear on the spawning turn")
	}
}
