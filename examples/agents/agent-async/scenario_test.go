package main

import (
	"strings"
	"testing"
)

// TestScenarioTranscript is the golden run: the deterministic StubProvider
// drives the subscribe → create_trigger → (event fires) → send_email flow,
// and the transcript must show the model setting up the standing behavior
// and then being woken to act on it.
func TestScenarioTranscript(t *testing.T) {
	out := &syncWriter{}
	if err := runScenario(out, nil); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{
		"subscribe_events",
		"create_trigger",
		"Set — I'll welcome-email every new user.",
		"long_report",
		"The quarterly report is ready",
		"· trigger: welcome", // the standing behavior fired
		"send_email",         // the proactive turn acted
		"Welcomed ada@example.com.",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
	// The event-driven welcome must come AFTER the setup, not before.
	if strings.Index(transcript, "· trigger: welcome") < strings.Index(transcript, "create_trigger") {
		t.Fatal("trigger fired before it was created")
	}
}
