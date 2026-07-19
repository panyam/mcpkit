package main

import (
	"strings"
	"testing"
)

// TestScenarioTranscript is the golden run: the deterministic StubProviders
// drive both composition modes, and the transcript must show the supervisor
// delegating to nested sub-agents and the team transferring control.
func TestScenarioTranscript(t *testing.T) {
	var b strings.Builder
	if err := runScenario(&b, nil); err != nil {
		t.Fatal(err)
	}
	transcript := b.String()

	for _, want := range []string{
		"── Supervisor (sub-agents as tools) ──",
		"[researcher] · calls web_search", // the sub-agent's event surfaced, nested
		"[researcher] → Go generics",      // and its answer
		"[coder] · calls run_code",
		"supervisor → ", // the supervisor synthesized
		"── Team (handoff) ──",
		"→ handed off: triage → billing", // control transferred, not returned
		"billing → I've refunded",        // the specialist took over the thread
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}

	// the researcher's nested tool call must precede the supervisor's synthesis
	if strings.Index(transcript, "[researcher] · calls") > strings.Index(transcript, "supervisor → ") {
		t.Fatal("sub-agent events should render before the supervisor's final answer")
	}
	// handoff transfers control: the specialist's answer is the final one
	if strings.Index(transcript, "→ handed off") > strings.Index(transcript, "billing → ") {
		t.Fatal("handoff should happen before the specialist answers")
	}
}
