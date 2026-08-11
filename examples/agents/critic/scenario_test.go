package main

import (
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
)

// TestCriticSteersAndSurfacesTheWall is the proof for issue 1148: it shows the
// watch-and-steer critic composes from public primitives on top of host.App,
// and it pins the one gap — delivery has no neutral injection seam.
func TestCriticSteersAndSurfacesTheWall(t *testing.T) {
	out := &syncWriter{}
	res, err := runScenario(out, nil, nil)
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}

	// (1) Watch + review works: the critic produced exactly one graded note,
	// and it is a concern about the over-broad delete.
	if len(res.delivered) != 1 {
		t.Fatalf("want 1 delivered note, got %d: %+v", len(res.delivered), res.delivered)
	}
	if res.delivered[0].Severity != SeverityConcern {
		t.Fatalf("want a concern, got %q", res.delivered[0].Severity)
	}
	if !strings.Contains(res.delivered[0].Note, "wipes ALL logs") {
		t.Fatalf("note lost its content: %q", res.delivered[0].Note)
	}

	// (2) Delivery reached the primary: the note text is present in some
	// request the primary model received on the second turn.
	noteFragment := "weigh, don't blindly obey"
	carrier := findNoteCarrier(res.primary.Requests(), noteFragment)
	if carrier == nil {
		t.Fatal("critic note never reached the primary model's context")
	}

	// (3) THE WALL: because host.App.RunTurn accepts only a plain string, the
	// note arrived as a RoleUser message, not a neutral RoleSystem nudge. If a
	// public injection seam existed, we would assert RoleSystem here instead.
	if carrier.Role != agent.RoleUser {
		t.Fatalf("expected the note to ride in as user text (the wall); got role %q\n"+
			"if this fails because it is now RoleSystem, the injection seam exists — update the finding",
			carrier.Role)
	}
}

// findNoteCarrier returns the first message across all requests whose text
// contains fragment, or nil.
func findNoteCarrier(reqs []agent.ProviderRequest, fragment string) *agent.Message {
	for _, req := range reqs {
		for i := range req.Messages {
			if strings.Contains(req.Messages[i].Text, fragment) {
				return &req.Messages[i]
			}
		}
	}
	return nil
}

// TestCriticGuardDropsAndRateLimits exercises the guard directly: content-free
// and duplicate notes are dropped, and a delivered concern opens an immune
// window that suppresses the next review.
func TestCriticGuardDropsAndRateLimits(t *testing.T) {
	c := newCritic(nil) // guard-only paths never touch the runner

	if _, ok := c.deliver(advisory{Severity: SeverityNone, Note: "all good"}, 0); ok {
		t.Fatal("severity none must be dropped")
	}
	if _, ok := c.deliver(advisory{Severity: SeverityAside, Note: "hi"}, 0); ok {
		t.Fatal("content-free (too short) must be dropped")
	}
	if _, ok := c.deliver(advisory{Severity: SeverityConcern, Note: "the delete is too broad"}, 1); !ok {
		t.Fatal("a real concern should deliver")
	}
	if _, ok := c.deliver(advisory{Severity: SeverityConcern, Note: "The  delete   is too BROAD"}, 2); ok {
		t.Fatal("normalized duplicate must be deduped")
	}
	// The concern at turn 1 set an immune window; a fresh note at turn 2 is
	// suppressed by the window, not by dedup.
	if c.quietUntil <= 2 {
		t.Fatalf("concern should open an immune window past turn 2, got quietUntil=%d", c.quietUntil)
	}
}
