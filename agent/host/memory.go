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

// injectMemoryLocked prepends the working-memory summary as a RoleSystem
// message when Config.Memory.InjectSummary is on, so the model stays aware
// of its scratchpad without a recall call. A summary error is non-fatal —
// memory awareness is best-effort and must never fail a turn. Caller holds
// turnMu.
func (a *App) injectMemoryLocked(ctx context.Context) {
	if a.memory == nil || a.cfg.Memory == nil || !a.cfg.Memory.InjectSummary {
		return
	}
	summary, err := a.memory.Summary(ctx)
	if err != nil || summary == "" {
		return
	}
	a.history = append(a.history, agent.Message{Role: agent.RoleSystem, Text: summary})
}
