package main

import (
	"context"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// Severity grades a critic note, following the vocabulary in issue 1148.
type Severity string

const (
	SeverityNone    Severity = "none"    // nothing worth saying this cycle
	SeverityAside   Severity = "aside"   // gentle, non-interrupting
	SeverityConcern Severity = "concern" // worth steering toward
	SeverityBlocker Severity = "blocker" // should not be waved past
)

// advisory is the critic model's single graded note per review cycle.
type advisory struct {
	Severity Severity `json:"severity"`
	Note     string   `json:"note"`
}

// criticSchema forces the critic model's answer into exactly {severity, note}
// via the Runner's ResponseSchema seam — no bespoke parsing, and an off-vocab
// severity is rejected by the enum before it reaches us.
var criticSchema = core.NewRawJSON([]byte(`{
  "type":"object",
  "properties":{
    "severity":{"type":"string","enum":["none","aside","concern","blocker"]},
    "note":{"type":"string"}
  },
  "required":["severity","note"],
  "additionalProperties":false
}`))

// criticInstructions is the critic model's system prompt. It watches; it never
// acts. It cannot approve or deny (that is the separate approval ladder) — its
// only channel is one graded note.
const criticInstructions = `You are a reviewer watching another agent work. You do not do the task.
Read the assistant's latest actions and reply with ONE short graded note.
- none: the actions look fine; say nothing of substance.
- aside: a minor, non-urgent observation.
- concern: something the assistant should reconsider before continuing.
- blocker: something that must not be ignored.
Be specific and terse. You cannot approve, deny, or run anything — you only advise.`

// critic is a second model that watches the primary's turns and returns one
// graded steering note per review. It is NOT an SDK type: it is built entirely
// from public primitives — a second agent.Runner (its own Provider +
// ResponseSchema) plus a small dedup/rate-limit guard the application keeps.
// That is the whole point of this example: no new "role" is needed to compose
// a watch-and-steer critic.
type critic struct {
	runner *agent.Runner

	// Guard knobs that keep the critic from nagging.
	immuneTurns int // stay quiet this many turns after a delivered concern/blocker
	minNoteLen  int // drop content-free notes shorter than this (normalized)
	ringSize    int // exact-dedup window

	recent     []string // normalized recent notes (ring buffer)
	quietUntil int      // primary turn index until which delivery is suppressed
}

func newCritic(r *agent.Runner) *critic {
	return &critic{runner: r, immuneTurns: 2, minNoteLen: 8, ringSize: 8}
}

// review runs the critic model over the transcript delta (the messages the
// primary just produced) and returns a note to deliver, or ok=false when the
// guard suppresses it. turn is the index of the primary turn just completed.
//
// Best-effort by contract: a critic failure returns ok=false, never an error —
// a watcher must never break the agent it watches.
func (c *critic) review(ctx context.Context, delta []agent.Message, turn int) (advisory, bool) {
	if turn < c.quietUntil {
		// Immune window: a concern/blocker just landed; give the primary
		// room to react before piling on.
		return advisory{}, false
	}
	if len(delta) == 0 {
		return advisory{}, false
	}

	// The critic sees ONLY the delta, framed as the material to review. Its
	// own past notes never enter here (they are delivered into the PRIMARY's
	// context, not appended to the primary's result messages), so it never
	// reviews itself.
	seed := []agent.Message{{Role: agent.RoleUser, Text: renderDelta(delta)}}
	res, err := c.runner.Run(ctx, seed, nil)
	if err != nil || res.Structured.Len() == 0 {
		return advisory{}, false
	}
	var adv advisory
	if err := res.Structured.Bind(&adv); err != nil {
		return advisory{}, false
	}
	return c.deliver(adv, turn)
}

// deliver applies the anti-nag guard to one advisory: normalize -> drop
// content-free -> exact-dedup -> rate-limit. It returns ok=false when the note
// should be suppressed, and opens an immune window after a delivered
// concern/blocker. Split out of review so it is testable without a model.
func (c *critic) deliver(adv advisory, turn int) (advisory, bool) {
	norm := normalize(adv.Note)
	if adv.Severity == SeverityNone || len(norm) < c.minNoteLen {
		return advisory{}, false
	}
	for _, seen := range c.recent {
		if seen == norm {
			return advisory{}, false
		}
	}
	c.remember(norm)
	if adv.Severity == SeverityConcern || adv.Severity == SeverityBlocker {
		c.quietUntil = turn + 1 + c.immuneTurns
	}
	return adv, true
}

func (c *critic) remember(norm string) {
	c.recent = append(c.recent, norm)
	if len(c.recent) > c.ringSize {
		c.recent = c.recent[len(c.recent)-c.ringSize:]
	}
}

func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// renderDelta turns the primary's new messages into the text the critic reviews.
func renderDelta(delta []agent.Message) string {
	var b strings.Builder
	for _, m := range delta {
		switch m.Role {
		case agent.RoleAssistant:
			if m.Text != "" {
				b.WriteString("assistant said: " + m.Text + "\n")
			}
			for _, tc := range m.ToolCalls {
				b.WriteString("assistant called tool " + tc.Name + " with args " + string(tc.Args.Raw()) + "\n")
			}
		case agent.RoleTool:
			b.WriteString("tool returned: " + m.Text + "\n")
		}
	}
	return b.String()
}
