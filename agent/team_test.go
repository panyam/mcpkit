package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func transferCall(id, target string) StubTurn {
	return StubTurn{ToolCalls: []ToolCall{{ID: id, Name: "transfer_to_" + target, Args: core.NewRawJSON(json.RawMessage(`{}`))}}}
}

func TestTeam_Handoff(t *testing.T) {
	var handoffs [][2]string
	// triage transfers to specialist, then the specialist answers.
	triage := NewStubProvider(transferCall("c1", "specialist"), StubTurn{Text: "let me connect you"})
	specialist := NewStubProvider(StubTurn{Text: "the specialist answer"})

	team, err := NewTeam(TeamConfig{
		Start: "triage",
		Members: []TeamMember{
			{Name: "triage", Config: RunnerConfig{Provider: triage}, HandoffTo: []string{"specialist"}},
			{Name: "specialist", Config: RunnerConfig{Provider: specialist}},
		},
		OnHandoff: func(from, to string) { handoffs = append(handoffs, [2]string{from, to}) },
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := team.Run(context.Background(), "help me", func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	// the specialist produced the final answer
	if result.Text != "the specialist answer" {
		t.Fatalf("final = %q, want the specialist's answer", result.Text)
	}
	// exactly one transfer, triage -> specialist
	if len(handoffs) != 1 || handoffs[0] != [2]string{"triage", "specialist"} {
		t.Fatalf("handoffs = %v, want [triage->specialist]", handoffs)
	}
	// context carried across: the specialist saw the original user message + triage's turn
	sawUser := false
	for _, m := range specialist.Requests()[0].Messages {
		if m.Role == RoleUser && m.Text == "help me" {
			sawUser = true
		}
	}
	if !sawUser {
		t.Fatal("handoff did not carry the conversation to the specialist")
	}
}

func TestTeam_NoHandoffReturnsDirectly(t *testing.T) {
	solo := NewStubProvider(StubTurn{Text: "answered directly"})
	team, _ := NewTeam(TeamConfig{
		Start:   "solo",
		Members: []TeamMember{{Name: "solo", Config: RunnerConfig{Provider: solo}}},
	})
	result, err := team.Run(context.Background(), "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answered directly" {
		t.Fatalf("no-handoff final = %q", result.Text)
	}
}

// TestTeam_TransferToolOnlyForAllowedAgents verifies the transfer tool is
// offered only to agents with a HandoffTo, and only for allowed targets.
func TestTeam_TransferToolOnlyForAllowedAgents(t *testing.T) {
	team, _ := NewTeam(TeamConfig{
		Start: "a",
		Members: []TeamMember{
			{Name: "a", Config: RunnerConfig{Provider: NewStubProvider()}, HandoffTo: []string{"b"}},
			{Name: "b", Config: RunnerConfig{Provider: NewStubProvider()}},
		},
	})
	defs, err := team.ToolDefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// a can transfer_to_b; b (terminal) offers no transfer tools
	if !hasToolNamed(defs["a"], "transfer_to_b") {
		t.Fatalf("agent a should offer transfer_to_b; got %v", toolNames(defs["a"]))
	}
	if len(defs["b"]) != 0 {
		t.Fatalf("terminal agent b should offer no tools; got %v", toolNames(defs["b"]))
	}
}

func hasToolNamed(defs []core.ToolDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func TestTeam_MaxHandoffsBoundsPingPong(t *testing.T) {
	// a and b hand off to each other forever; the cap stops it.
	a := NewStubProvider(transferCall("1", "b"), StubTurn{Text: "x"}, transferCall("2", "b"), StubTurn{Text: "x"})
	b := NewStubProvider(transferCall("1", "a"), StubTurn{Text: "x"}, transferCall("2", "a"), StubTurn{Text: "x"})
	team, _ := NewTeam(TeamConfig{
		Start:       "a",
		MaxHandoffs: 2,
		Members: []TeamMember{
			{Name: "a", Config: RunnerConfig{Provider: a}, HandoffTo: []string{"b"}},
			{Name: "b", Config: RunnerConfig{Provider: b}, HandoffTo: []string{"a"}},
		},
	})
	_, err := team.Run(context.Background(), "loop", nil)
	if !errors.Is(err, ErrMaxHandoffs) {
		t.Fatalf("a transfer loop should hit ErrMaxHandoffs, got %v", err)
	}
}

func TestTeam_Validation(t *testing.T) {
	stub := func() *StubProvider { return NewStubProvider(StubTurn{Text: "x"}) }
	cases := []struct {
		name string
		cfg  TeamConfig
	}{
		{"no members", TeamConfig{Start: "a"}},
		{"unknown start", TeamConfig{Start: "z", Members: []TeamMember{{Name: "a", Config: RunnerConfig{Provider: stub()}}}}},
		{"duplicate name", TeamConfig{Start: "a", Members: []TeamMember{
			{Name: "a", Config: RunnerConfig{Provider: stub()}}, {Name: "a", Config: RunnerConfig{Provider: stub()}}}}},
		{"handoff to unknown", TeamConfig{Start: "a", Members: []TeamMember{
			{Name: "a", Config: RunnerConfig{Provider: stub()}, HandoffTo: []string{"ghost"}}}}},
	}
	for _, c := range cases {
		if _, err := NewTeam(c.cfg); err == nil {
			t.Fatalf("%s: expected a construction error", c.name)
		}
	}
}
