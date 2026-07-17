package main

import (
	"strings"
	"testing"
)

// TestAgentScenarioTranscript is the golden run: the host connects to the
// in-process skills server, digest-verifies and injects the server's skills
// into the model's instructions, and the agent answers using them. The
// transcript must show the skills being loaded and the convention-following
// replies.
func TestAgentScenarioTranscript(t *testing.T) {
	out := &syncWriter{}
	if err := runAgentScenario(out, nil); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{
		"loaded from team", // the host injected the server's skills before any turn
		"one per logical change",
		"feature branch",           // git-workflow skill guidance
		"scripts/extract.py",       // pdf-processing skill guidance
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
	// Skills must be loaded before the first answer that relies on them.
	if strings.Index(transcript, "loaded from team") > strings.Index(transcript, "one per logical change") {
		t.Fatal("skills loaded after the agent already answered")
	}
}
