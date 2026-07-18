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
	if len(got.Items) != 1 || got.Items[0].Item.Key != "lang" || got.Items[0].Item.Value != "Go" {
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

// TestAppMemorySummaryBudget wires SummaryMaxItems through the host: with two
// notes stored and a cap of 1, the injected summary carries only the newest.
func TestAppMemorySummaryBudget(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"old","value":"first"}`)),
		}}},
		agent.StubTurn{Text: "saved"},
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c2", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"new","value":"second"}`)),
		}}},
		agent.StubTurn{Text: "saved"},
		agent.StubTurn{Text: "reply3"},
	)
	cfg := memoryConfig(ts.URL, true)
	cfg.Memory.SummaryMaxItems = 1
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	for _, in := range []string{"remember old", "remember new", "anything?"} {
		if err := app.RunTurn(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}

	// turn 3's request (index 4) carries the budgeted summary: newest only
	msgs := stub.Requests()[4].Messages
	if !hasSystemContaining(msgs, "new: second") {
		t.Fatal("budgeted summary should include the newest note")
	}
	if hasSystemContaining(msgs, "old: first") {
		t.Fatal("SummaryMaxItems=1 should drop the older note from the injection")
	}
}

// TestAppInjectsRelevantRecall is the 940 PR2 payoff: with InjectRecall on
// and a semantic store, the model receives the note relevant to the user's
// message as background context WITHOUT calling the recall tool — and the
// injected block is transient (never written into a.history).
func TestAppInjectsRelevantRecall(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "you use Go"})

	store, err := agent.NewInMemorySemanticStore(agent.StubEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.PutMemory(context.Background(), agent.PutMemoryRequest{
		Item: agent.MemoryItem{Key: "lang", Value: "my favorite programming language is Go"}})

	cfg := testConfig(ts.URL)
	cfg.Memory = &MemoryConfig{InjectRecall: true}
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(stub), WithMemoryStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "which programming language do i use?"); err != nil {
		t.Fatal(err)
	}

	// the model saw the relevant note injected as context
	msgs := stub.Requests()[0].Messages
	if !hasSystemContaining(msgs, "Relevant to the current message") || !hasSystemContaining(msgs, "lang: my favorite") {
		t.Fatalf("relevant recall was not injected into the model request: %+v", msgs)
	}
	// transient: the injected block is not persisted into history
	if countSystemContaining(app.history, "Relevant to the current message") != 0 {
		t.Fatal("recall injection must be transient, not written to a.history")
	}
}

func hasSystemContaining(msgs []agent.Message, sub string) bool {
	return countSystemContaining(msgs, sub) > 0
}

func countSystemContaining(msgs []agent.Message, sub string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == agent.RoleSystem && strings.Contains(m.Text, sub) {
			n++
		}
	}
	return n
}

// TestMemorySummaryIsTransient proves the injected summary never lands in the
// persistent history and never accumulates: over three turns with a fact
// remembered on turn 1, a.history holds zero summaries and every later turn's
// model request carries exactly one (not a growing stack).
func TestMemorySummaryIsTransient(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		// turn 1: remember, then reply
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"lang","value":"Go"}`)),
		}}},
		agent.StubTurn{Text: "saved"},
		// turns 2 and 3: plain replies — each should see exactly one summary
		agent.StubTurn{Text: "reply2"},
		agent.StubTurn{Text: "reply3"},
	)
	var out strings.Builder
	app, err := NewApp(memoryConfig(ts.URL, true), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	for _, in := range []string{"remember Go", "and?", "still there?"} {
		if err := app.RunTurn(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}

	const marker = "Working memory"
	// the durable history never carries a summary
	if n := countSystemContaining(app.history, marker); n != 0 {
		t.Fatalf("a.history holds %d summary messages, want 0 (must be transient)", n)
	}
	// turns 2 and 3 (requests[2], [3]) each carry exactly one — no accumulation
	for _, i := range []int{2, 3} {
		if n := countSystemContaining(stub.Requests()[i].Messages, marker); n != 1 {
			t.Fatalf("request[%d] carried %d summaries, want exactly 1", i, n)
		}
	}
}
