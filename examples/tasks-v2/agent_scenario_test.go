package main

import (
	"strings"
	"testing"
)

// TestAgentScenarioTranscript is the golden run: the deterministic StubProvider
// drives a sync-only tool call and a server-directed async (task-backed) one,
// and the transcript must show the model calling both the same way while the
// task machinery stays invisible to the model.
func TestAgentScenarioTranscript(t *testing.T) {
	out := &syncWriter{}
	if err := runAgentScenario(out, nil); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{
		"greet",                // sync-only tool
		"Hello, Ada!",          // its result flowed back
		"Said hello to Ada.",   // the model's reply
		"slow_compute",         // the task-backed tool
		"task",                 // the host ran it as a SEP-2663 task
		"Result: 42.",          // the task result flowed back like any tool result
		"The quarterly computation is done — the result is 42.",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
	// The task-backed call must come after the sync one (turn order preserved).
	if strings.Index(transcript, "slow_compute") < strings.Index(transcript, "greet") {
		t.Fatal("task tool ran before the sync tool")
	}
}
