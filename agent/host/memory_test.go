package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

func memoryConfig(url string, injectSummary bool) *Config {
	c := testConfig(url)
	c.Memory = &MemoryConfig{InjectSummary: injectSummary}
	return c
}

// TestAppWorkingMemoryAcrossTurns is the StubProvider eval for issue 938:
// the model remembers a fact on one turn and recalls it on a later turn,
// proving the scratchpad survives across turns through the tools seam.
func TestAppWorkingMemoryAcrossTurns(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		// turn 1: the model saves a note
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"lang","value":"Go"}`)),
		}}},
		agent.StubTurn{Text: "saved"},
		// turn 2 (a later RunTurn): the model recalls it
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c2", Name: agent.RecallToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"query":"lang"}`)),
		}}},
		agent.StubTurn{Text: "it is Go"},
	)
	store := agent.NewInMemoryMemoryStore()
	var out strings.Builder
	app, err := NewApp(memoryConfig(ts.URL, false), &out, strings.NewReader(""),
		WithProvider(stub), WithMemoryStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "remember my language is Go"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(context.Background(), "what is my language?"); err != nil {
		t.Fatal(err)
	}

	// the recall tool result fed back on the 4th model call carries the value.
	// request[3].Messages holds the full history, so match the recall call by
	// its id (c2) rather than taking the first RoleTool (the earlier remember).
	fed := toolMsgByID(t, stub.Requests()[3].Messages, "c2").Text
	if !strings.Contains(fed, "Go") {
		t.Fatalf("recall did not return the stored value; tool message = %q", fed)
	}

	// and the store actually holds it
	got, _ := store.ListMemories(context.Background(), agent.ListMemoriesRequest{})
	if len(got.Items) != 1 || got.Items[0].Key != "lang" || got.Items[0].Value != "Go" {
		t.Fatalf("store = %+v, want lang=Go", got.Items)
	}
}

func TestAppMemorySummaryInjection(t *testing.T) {
	ts := startTestServer(t)
	script := func() *agent.StubProvider {
		return agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{
				ID: "c1", Name: agent.RememberToolName,
				Args: core.NewRawJSON(json.RawMessage(`{"key":"lang","value":"Go"}`)),
			}}},
			agent.StubTurn{Text: "saved"},
			agent.StubTurn{Text: "second turn reply"},
		)
	}

	const marker = "Working memory"

	// injection ON: the second turn's first model call carries a RoleSystem summary
	on := script()
	var out strings.Builder
	appOn, err := NewApp(memoryConfig(ts.URL, true), &out, strings.NewReader(""), WithProvider(on))
	if err != nil {
		t.Fatal(err)
	}
	defer appOn.Close()
	_ = appOn.RunTurn(context.Background(), "remember Go")
	_ = appOn.RunTurn(context.Background(), "anything new?")
	if !hasSystemContaining(on.Requests()[2].Messages, marker) {
		t.Fatalf("InjectSummary on: expected a RoleSystem %q message on the second turn", marker)
	}

	// injection OFF (default): no summary is injected
	off := script()
	var out2 strings.Builder
	appOff, err := NewApp(memoryConfig(ts.URL, false), &out2, strings.NewReader(""), WithProvider(off))
	if err != nil {
		t.Fatal(err)
	}
	defer appOff.Close()
	_ = appOff.RunTurn(context.Background(), "remember Go")
	_ = appOff.RunTurn(context.Background(), "anything new?")
	if hasSystemContaining(off.Requests()[2].Messages, marker) {
		t.Fatal("InjectSummary off: no memory summary should be injected")
	}
}

func toolMsgByID(t *testing.T, msgs []agent.Message, id string) agent.Message {
	t.Helper()
	for _, m := range msgs {
		if m.Role == agent.RoleTool && m.ToolCallID == id {
			return m
		}
	}
	t.Fatalf("no RoleTool message with id %q in %+v", id, msgs)
	return agent.Message{}
}

func hasSystemContaining(msgs []agent.Message, sub string) bool {
	for _, m := range msgs {
		if m.Role == agent.RoleSystem && strings.Contains(m.Text, sub) {
			return true
		}
	}
	return false
}
