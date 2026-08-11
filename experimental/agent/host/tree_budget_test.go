package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

// TestAppTreeBudgetBoundsTheTurn is the 1032 host wiring: Config.MaxTreeSteps
// reaches the main Runner as a TreeBudget, so a chatty agent is stopped at the
// aggregate cap (here it spins by calling an unknown tool, which continues the
// turn) rather than running to its own MaxSteps.
func TestAppTreeBudgetBoundsTheTurn(t *testing.T) {
	ts := startTestServer(t)
	turns := make([]agent.StubTurn, 10)
	for i := range turns {
		turns[i] = agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "x", Name: "nope", Args: core.NewRawJSON(json.RawMessage(`{}`)),
		}}}
	}
	stub := agent.NewStubProvider(turns...)

	cfg := testConfig(ts.URL)
	cfg.MaxSteps = 20
	cfg.MaxTreeSteps = 3
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "go"); err == nil {
		t.Fatal("a turn exceeding the tree step budget should fail")
	}
	if n := len(stub.Requests()); n != 3 {
		t.Fatalf("tree budget should have capped the turn at 3 model calls, got %d", n)
	}
}
