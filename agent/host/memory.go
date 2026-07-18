package host

import (
	"context"

	"github.com/panyam/mcpkit/agent"
)

// memorySourceID is the MultiSource id the working-memory tools register
// under, alongside the "host" meta-tools and the per-server ids.
const memorySourceID = "memory"

// WithMemoryStore supplies the backing store for working memory
// (Config.Memory). Omitted, memory uses an in-memory store that dies with
// the process; a durable, session-scoped backend is a follow-up. Ignored
// when Config.Memory is nil.
func WithMemoryStore(store agent.MemoryStore) AppOption {
	return func(o *appOptions) { o.memoryStore = store }
}

// registerMemory builds the MemorySource over store (in-memory when nil)
// and adds it to multi so its remember/recall/forget tools reach the model.
// The source is held on the App for summary injection and the /memory
// command.
func (a *App) registerMemory(multi *agent.MultiSource, store agent.MemoryStore) error {
	if store == nil {
		store = agent.NewInMemoryMemoryStore()
	}
	src, err := agent.NewMemorySource(store)
	if err != nil {
		return err
	}
	if err := multi.Add(memorySourceID, src); err != nil {
		return err
	}
	a.memory = src
	return nil
}

// withMemorySummaryLocked returns the messages for this turn, weaving the
// working-memory summary in as a RoleSystem message just before the current
// (last) user message when Config.Memory.InjectSummary is on.
//
// The summary is a stateful snapshot of current memory, re-rendered every
// turn, so it is injected TRANSIENTLY: it goes into the returned slice for
// this one model call and is never written into a.history. That keeps the
// durable conversation (and the persisted RunStore log) free of stacked,
// mostly-identical snapshots — the opposite of drainInjectionLocked, which
// appends because each event is drained exactly once.
//
// When memory is off or empty it returns a.history unchanged (no copy). A
// summary error is non-fatal — memory awareness is best-effort and must
// never fail a turn. Caller holds turnMu and has already appended the user
// message to a.history.
func (a *App) withMemorySummaryLocked(ctx context.Context) []agent.Message {
	if a.memory == nil || a.cfg.Memory == nil || !a.cfg.Memory.InjectSummary {
		return a.history
	}
	summary, err := a.memory.Summary(ctx, a.cfg.Memory.summaryOptions())
	if err != nil || summary == "" {
		return a.history
	}
	n := len(a.history)
	msgs := make([]agent.Message, 0, n+1)
	msgs = append(msgs, a.history[:n-1]...)
	msgs = append(msgs, agent.Message{Role: agent.RoleSystem, Text: summary})
	msgs = append(msgs, a.history[n-1])
	return msgs
}
