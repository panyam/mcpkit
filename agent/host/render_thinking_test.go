package host

import (
	"os"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

// TestRenderer_ThinkingStreamsText pins that reasoning deltas render as the
// reasoning text (dimmed), not a row of dots — so a reasoning model's
// chain-of-thought is readable in plain and TUI output.
func TestRenderer_ThinkingStreamsText(t *testing.T) {
	os.Setenv("NO_COLOR", "1") // strip ANSI so we assert on plain text
	defer os.Unsetenv("NO_COLOR")
	var b strings.Builder
	r := newRenderer(&b)
	r.handle(agent.Event{Kind: agent.EventThinkingBegin})
	r.handle(agent.Event{Kind: agent.EventThinkingDelta, Text: "weigh option A "})
	r.handle(agent.Event{Kind: agent.EventThinkingDelta, Text: "vs option B"})
	r.handle(agent.Event{Kind: agent.EventThinkingEnd})
	r.handle(agent.Event{Kind: agent.EventTextDelta, Text: "Go with A."})
	out := b.String()
	if !strings.Contains(out, "· thinking:") {
		t.Fatalf("missing thinking header: %q", out)
	}
	if !strings.Contains(out, "weigh option A vs option B") {
		t.Fatalf("reasoning text not streamed: %q", out)
	}
	if !strings.Contains(out, "Go with A.") {
		t.Fatalf("answer missing: %q", out)
	}
}
