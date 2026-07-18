package host

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func compactionConfig(url string, maxTokens, keepRecent int) *Config {
	c := testConfig(url)
	c.Compaction = &CompactionConfig{MaxTokens: maxTokens, KeepRecent: keepRecent}
	return c
}

func TestAppCompactsWhenOverBudget(t *testing.T) {
	ts := startTestServer(t)
	// One StubProvider serves both roles (shared turn counter): the first
	// call is the summarizer Generate, the second is the chat Stream.
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "user prefers Go; earlier context compacted"},
		agent.StubTurn{Text: "final answer"},
	)
	var out strings.Builder
	// tiny budget so the very first turn's history trips compaction
	app, err := NewApp(compactionConfig(ts.URL, 5, 1), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// seed a long-ish history so the estimate exceeds the budget
	app.history = []agent.Message{
		{Role: agent.RoleUser, Text: strings.Repeat("long earlier context ", 5)},
		{Role: agent.RoleAssistant, Text: strings.Repeat("long earlier reply ", 5)},
	}

	if err := app.RunTurn(context.Background(), "what now?"); err != nil {
		t.Fatal(err)
	}

	// the chat model (second call) saw the compacted summary, not the raw head
	sawSummary := false
	for _, m := range stub.Requests()[1].Messages {
		if m.Role == agent.RoleSystem && strings.Contains(m.Text, "earlier context compacted") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatal("chat model did not receive the compacted summary")
	}
}

func TestAppRejectsCompactionWithoutMaxTokens(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "x"})
	var out strings.Builder
	// MaxTokens unset (0) is invalid — NewApp must surface the error
	_, err := NewApp(compactionConfig(ts.URL, 0, 2), &out, strings.NewReader(""), WithProvider(stub))
	if err == nil {
		t.Fatal("expected NewApp to reject compaction without MaxTokens")
	}
}
