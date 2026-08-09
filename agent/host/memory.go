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
// the process; pass a durable one (agent/store/redis, agent/store/gorm) to
// survive restarts. Per-session isolation is orthogonal — set
// MemoryConfig.SessionScoped to namespace the store by run id. Ignored when
// Config.Memory is nil.
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
	var opts []agent.MemorySourceOption
	if a.cfg.Memory != nil && a.cfg.Memory.SessionScoped {
		// currentRunID is lock-free: it runs during a turn while turnMu is
		// already held (both the memory tools and the summary/recall injection),
		// so RunID would deadlock here.
		opts = append(opts, agent.WithMemoryNamespaceFunc(a.currentRunID))
		if a.store == nil {
			a.log.Warn("host: memory sessionScoped is set but no RunStore is configured; every session shares the default scratchpad (add WithRunStore / --session-store to isolate)")
		}
	}
	src, err := agent.NewMemorySource(store, opts...)
	if err != nil {
		return err
	}
	if err := multi.Add(memorySourceID, src); err != nil {
		return err
	}
	a.memory = src
	return nil
}

// memoryStages are the transient context producers memory contributes: the
// ambient scratchpad summary, then the recall relevant to this turn. Both are
// woven in just before the user message, recall closest because it was
// retrieved for these exact words while the summary is ambient.
//
// Neither is written into history — see contextPipeline on why that split is
// structural. A producer whose store errors contributes nothing rather than
// failing the turn.
func (a *App) memoryStages() []contextStage {
	if a.memory == nil || a.cfg.Memory == nil {
		return nil
	}
	var out []contextStage
	if a.cfg.Memory.InjectSummary {
		out = append(out, contextStage{name: "memory.summary", run: func(ctx context.Context, msgs []agent.Message) []agent.Message {
			if len(msgs) == 0 {
				return msgs
			}
			s, err := a.memory.Summary(ctx, a.cfg.Memory.summaryOptions())
			if err != nil || s == "" {
				return msgs
			}
			return weaveBeforeUser(msgs, []string{s})
		}})
	}
	if a.cfg.Memory.InjectRecall {
		out = append(out, contextStage{name: "memory.recall", run: func(ctx context.Context, msgs []agent.Message) []agent.Message {
			if len(msgs) == 0 {
				return msgs
			}
			r, err := a.memory.RecallRelevant(ctx, msgs[len(msgs)-1].Text, a.cfg.Memory.recallOptions())
			if err != nil || r == "" {
				return msgs
			}
			return weaveBeforeUser(msgs, []string{r})
		}})
	}
	return out
}
