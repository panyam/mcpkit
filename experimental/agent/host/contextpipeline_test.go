package host

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
)

func stubStage(name, text string) ContextStage {
	return ContextStage{Name: name, Run: func(_ context.Context, msgs []agent.Message) []agent.Message {
		return weaveBeforeUser(msgs, []string{text})
	}}
}

func texts(msgs []agent.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Text)
	}
	return out
}

// TestPipelineDurableAppendsBeforeUser pins that durable stages contribute
// history that precedes the user's message: events describe what happened
// before the user spoke, so they cannot land after it.
func TestPipelineDurableAppendsBeforeUser(t *testing.T) {
	p := contextPipeline{durable: []ContextStage{
		{Name: "events", Run: func(_ context.Context, msgs []agent.Message) []agent.Message {
			return append(msgs, agent.Message{Role: agent.RoleSystem, Text: "an event"})
		}},
	}}
	got := p.runDurable(context.Background(), nil, agent.Message{Role: agent.RoleUser, Text: "hello"})
	if want := []string{"an event", "hello"}; !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("durable order = %v, want %v", texts(got), want)
	}
}

// TestPipelineTransientWeavesBeforeUserInOrder pins the salience rule: each
// transient producer inserts just before the user's message, so the last
// stage registered ends up closest to it. That is why recall follows the
// summary rather than preceding it.
func TestPipelineTransientWeavesBeforeUserInOrder(t *testing.T) {
	p := contextPipeline{transient: []ContextStage{
		stubStage("memory.summary", "SUMMARY"),
		stubStage("memory.recall", "RECALL"),
	}}
	history := []agent.Message{{Role: agent.RoleUser, Text: "hello"}}
	got := p.runTransient(context.Background(), history)
	if want := []string{"SUMMARY", "RECALL", "hello"}; !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("transient order = %v, want %v", texts(got), want)
	}
}

// TestPipelineTransientNeverMutatesHistory is the invariant the whole
// durable/transient split exists to protect. A transient block written back
// into history would be re-injected next turn and eventually summarized
// alongside real conversation, so the per-turn view must not alias it.
func TestPipelineTransientNeverMutatesHistory(t *testing.T) {
	p := contextPipeline{transient: []ContextStage{stubStage("memory.recall", "RECALL")}}
	history := []agent.Message{{Role: agent.RoleUser, Text: "hello"}}

	perTurn := p.runTransient(context.Background(), history)

	if len(history) != 1 || history[0].Text != "hello" {
		t.Fatalf("history was mutated: %v", texts(history))
	}
	if len(perTurn) != 2 {
		t.Fatalf("per-turn view = %v, want the injected block plus the user message", texts(perTurn))
	}
	// Writing through the per-turn slice must not reach history's array.
	perTurn[len(perTurn)-1].Text = "clobbered"
	if history[0].Text != "hello" {
		t.Fatal("per-turn slice aliases history's backing array")
	}
}

// TestPipelineEmptyStagesArePassThrough pins that a host with no producers
// configured pays nothing: history is returned unchanged and the per-turn
// view is just a copy.
func TestPipelineEmptyStagesArePassThrough(t *testing.T) {
	var p contextPipeline
	want := []string{"hi"}
	history := p.runDurable(context.Background(), nil, agent.Message{Role: agent.RoleUser, Text: "hi"})
	if !reflect.DeepEqual(texts(history), want) {
		t.Fatalf("durable = %v, want %v", texts(history), want)
	}
	if got := p.runTransient(context.Background(), history); !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("transient = %v, want %v", texts(got), want)
	}
}

// TestDeclaredStageOrder pins the assembled pipeline a real App builds, which
// is the whole point of the issue: the order is declared in one place instead
// of emerging from where the code happened to sit.
func TestDeclaredStageOrder(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Memory = &MemoryConfig{InjectSummary: true, InjectRecall: true}

	app, err := NewApp(cfg, &strings.Builder{}, strings.NewReader(""),
		WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "hi"})))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	want := []string{"events", "memory.summary", "memory.recall"}
	if got := app.context.stageNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("declared stage order = %v, want %v", got, want)
	}
}

// TestMemoryStagesAbsentWhenMemoryOff pins that a disabled producer
// contributes no stage at all, rather than a stage that runs and no-ops.
func TestMemoryStagesAbsentWhenMemoryOff(t *testing.T) {
	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), &strings.Builder{}, strings.NewReader(""),
		WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "hi"})))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if want := []string{"events"}; !reflect.DeepEqual(app.context.stageNames(), want) {
		t.Fatalf("stage order with memory off = %v, want %v", app.context.stageNames(), want)
	}
}
