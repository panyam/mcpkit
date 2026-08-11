package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

// TestAppFanOutDelegatesToAllMembers is the 1033 host payoff: a FanOut group
// config exposes one tool the main agent calls once, and the task reaches every
// member (each surfaces a scoped HostSubAgentEvent), with their answers folded
// into the aggregate fed back to the main agent. Concurrency itself is proven at
// the primitive layer (TestFanOutRunsMembersConcurrently); this covers wiring.
func TestAppFanOutDelegatesToAllMembers(t *testing.T) {
	ts := startTestServer(t)

	// One StubProvider serves the main agent and both members. The two member
	// text turns are consumed in whichever order the members reach the provider
	// (concurrent), so the test does not bind a member to a specific text.
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "f1", Name: "review_all",
			Args: core.NewRawJSON(json.RawMessage(`{"task":"review the plan"}`)),
		}}},
		agent.StubTurn{Text: "finding-A"},
		agent.StubTurn{Text: "finding-B"},
		agent.StubTurn{Text: "here is the combined review"},
	)

	cfg := testConfig(ts.URL)
	cfg.FanOut = []FanOutGroupConfig{{
		Name:        "review_all",
		Description: "ask every reviewer at once",
		Members: []SubAgentConfig{
			{Name: "rev1", Description: "reviewer one", Instructions: "review."},
			{Name: "rev2", Description: "reviewer two", Instructions: "review."},
		},
	}}
	obs := &collectObserver{}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// the fan-out group is exposed to the main agent as a single tool, and its
	// members are NOT exposed as individual delegate tools.
	defs, _ := app.sources.Tools(context.Background())
	if !hasToolNamed(defs, "review_all") {
		t.Fatalf("fan-out tool 'review_all' not offered: %v", toolDefNames(defs))
	}
	if hasToolNamed(defs, "rev1") || hasToolNamed(defs, "rev2") {
		t.Fatalf("fan-out members must not be separately offered: %v", toolDefNames(defs))
	}

	if err := app.RunTurn(context.Background(), "review my plan"); err != nil {
		t.Fatal(err)
	}

	// both members ran — each surfaced a scoped HostSubAgentEvent
	scopes := map[string]bool{}
	for _, e := range obs.kinds(HostSubAgentEvent) {
		scopes[e.SubAgent.Scope] = true
	}
	if !scopes["rev1"] || !scopes["rev2"] {
		t.Fatalf("expected sub-agent events scoped to both members, saw %v", scopes)
	}

	// the aggregate fed back to the main agent carries every member's answer
	fed := toolMsgByID(t, stub.Requests()[3].Messages, "f1").Text
	if !strings.Contains(fed, "finding-A") || !strings.Contains(fed, "finding-B") {
		t.Fatalf("aggregate missing a member's answer: %q", fed)
	}
	if !strings.Contains(fed, "[rev1]") || !strings.Contains(fed, "[rev2]") {
		t.Fatalf("aggregate missing member labels: %q", fed)
	}
}
