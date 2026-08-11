package host

import (
	"context"

	"github.com/panyam/mcpkit/experimental/agent"
)

// ContextStage is one named step of pre-turn context assembly: it takes the
// messages so far and returns them with its own contribution folded in.
//
// A stage that has nothing to add returns its input unchanged. Stages do not
// report errors: a producer that cannot run (a store that is down, an
// embedder that timed out) contributes nothing rather than failing the turn,
// because context assembly is best-effort by nature and a missing recall
// block is a worse answer, not a broken one.
type ContextStage struct {
	// Name identifies the stage in stageNames and in diagnostics. Use a
	// dotted form scoped to the contributor ("memory.recall") so a pipeline
	// listing says who added what.
	Name string

	// Run folds this stage's contribution into the messages so far.
	Run func(ctx context.Context, msgs []agent.Message) []agent.Message
}

// contextPipeline declares, in one place, how a turn's context is assembled
// and why it is assembled in that order.
//
// Before this existed the order was real but implicit, accreted across two
// layers: host RunTurn drained events, appended the user message, then wove in
// the memory summary and recall, while the Runner compacted at the top of its
// loop. Every individual ordering was defensible; the structure was never
// designed, and a new stage had no declared place to go.
//
// # The two phases
//
// Durable stages produce messages that join the session's history and persist:
// injected events, then the user's own message. Transient stages produce a
// per-turn view that the model sees once and that is never written back:
// the memory summary and relevance recall.
//
// The split is structural rather than a convention, because getting it wrong
// is silent and compounding. A transient block written into history would be
// re-injected next turn, then summarized alongside real conversation, and the
// scratchpad would slowly become indistinguishable from what the user said.
// Two slices make that mistake unrepresentable instead of merely discouraged.
//
// # Order, and why
//
//	events -> user -> summary -> recall -> [compaction, Runner-side]
//
// Events precede the user message because they describe what happened before
// the user spoke. Recall follows the summary and sits closest to the user
// message because it is the most salient: it was retrieved for this turn
// specifically, while the summary is ambient. Both are woven in just before
// the user message rather than appended after it, so the user's words remain
// the last thing the model reads.
//
// # Compaction stays in the Runner, and runs last
//
// Compaction is named here as the terminal fit-to-budget stage but is not
// executed here: it lives on the Runner so that an eval can grade a compaction
// strategy without standing a host up. Naming it keeps the pipeline honest
// about what actually happens to the messages after assemble returns.
//
// That it runs last is now a decision rather than an accident of where the
// code sat. inject-then-compact guarantees the assembled context fits the
// budget, at the cost that a freshly retrieved block can fall into the
// compacted head and be summarized — a wasted model call on content that was
// already a summary. compact-then-inject keeps retrievals verbatim but lets
// injections push the result back over the budget after it was fit, which
// fails the turn outright at the provider.
//
// A degraded answer beats a failed one, so inject-then-compact wins. The cost
// is real though, and it has a sharp edge worth knowing: SummarizingCompactor
// keeps its most recent KeepRecent messages verbatim, so injected blocks
// survive only while KeepRecent exceeds the number of blocks plus the user
// message. With a small KeepRecent and several producers, this turn's recall
// can be summarized the moment it is created. See RunnerConfig.Compactor.
//
// # Adding a stage
//
// Append to the phase whose persistence it wants, at the position its
// dependencies require. The injection-budget arbiter (#1024) is a transient
// stage that would run after every producer and before compaction, once
// contention between producers is measurable enough to rank them on a common
// scale.
type contextPipeline struct {
	durable   []ContextStage
	transient []ContextStage
}

// runDurable applies the durable stages and appends the user's message,
// returning what the session should keep as its history.
//
// Separate from runTransient rather than one assemble call, because the host
// must do real work between the two phases. The run id is created lazily on
// the first persisted turn, and session-scoped memory derives its namespace
// from that id — so recall running before the run exists would query the
// shared default scratchpad instead of this session's. The phases are two
// methods because there is a mandatory step between them, not because the
// pipeline wanted splitting.
func (p *contextPipeline) runDurable(ctx context.Context, prior []agent.Message, user agent.Message) []agent.Message {
	out := prior
	for _, st := range p.durable {
		out = st.Run(ctx, out)
	}
	return append(out, user)
}

// runTransient weaves the per-turn producers into a copy of history and
// returns what the model sees now. The result never aliases history's backing
// array, so the caller can keep one and send the other without either
// corrupting the other.
func (p *contextPipeline) runTransient(ctx context.Context, history []agent.Message) []agent.Message {
	out := make([]agent.Message, len(history))
	copy(out, history)
	for _, st := range p.transient {
		out = st.Run(ctx, out)
	}
	return out
}

// stageNames lists the pipeline in execution order, durable phase first, for
// diagnostics and tests that pin the declared order.
func (p *contextPipeline) stageNames() []string {
	out := make([]string, 0, len(p.durable)+len(p.transient))
	for _, st := range p.durable {
		out = append(out, st.Name)
	}
	for _, st := range p.transient {
		out = append(out, st.Name)
	}
	return out
}

// weaveBeforeUser inserts blocks as RoleSystem messages immediately before the
// final message, which by construction is the user's. Shared by every
// transient producer so "closest to the user message is most salient" is
// implemented once rather than re-derived per stage.
func weaveBeforeUser(msgs []agent.Message, blocks []string) []agent.Message {
	if len(blocks) == 0 || len(msgs) == 0 {
		return msgs
	}
	n := len(msgs)
	out := make([]agent.Message, 0, n+len(blocks))
	out = append(out, msgs[:n-1]...)
	for _, b := range blocks {
		out = append(out, agent.Message{Role: agent.RoleSystem, Text: b})
	}
	out = append(out, msgs[n-1])
	return out
}
