package host

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/agent"
)

// WithRunStore enables session persistence: every completed turn's
// messages (and its event stream) are appended to a run in store, so a
// surface can resume or fork the session later — across process
// restarts when the store is durable. Nil (the default) keeps the
// pre-persistence behavior: history lives only in memory.
func WithRunStore(store agent.RunStore) AppOption {
	return func(o *appOptions) { o.store = store }
}

// RunID returns the active persisted run, or "" when persistence is off
// or no run has been attached or created yet (the first turn creates
// one lazily).
func (a *App) RunID() string {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	return a.runID
}

// AttachRun binds the session to runID with create-or-resume semantics:
// an existing run's history is loaded and threaded (resume), an absent
// one is created empty. This is the startup path for surfaces that name
// sessions ("continue yesterday's run-x or start it fresh"); for a
// must-exist switch mid-session, use Resume.
func (a *App) AttachRun(ctx context.Context, runID string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.store == nil {
		return fmt.Errorf("host: no RunStore configured")
	}
	resp, err := a.store.CreateRun(ctx, agent.CreateRunRequest{RunID: runID})
	if err != nil {
		return fmt.Errorf("host: attaching run %q: %w", runID, err)
	}
	if resp.Created {
		a.runID = resp.RunID
		a.history = nil
		return nil
	}
	return a.resumeLocked(ctx, runID)
}

// Resume switches the session to an existing run: its persisted
// messages replace the in-memory history and subsequent turns append to
// it. An unknown runID is an error (nothing changes), so a typo cannot
// silently start an empty session — use AttachRun for create-or-resume.
func (a *App) Resume(ctx context.Context, runID string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.store == nil {
		return fmt.Errorf("host: no RunStore configured")
	}
	return a.resumeLocked(ctx, runID)
}

func (a *App) resumeLocked(ctx context.Context, runID string) error {
	resp, err := a.store.LoadRun(ctx, agent.LoadRunRequest{RunID: runID})
	if err != nil {
		return fmt.Errorf("host: loading run %q: %w", runID, err)
	}
	if !resp.Found {
		return fmt.Errorf("host: run %q not found", runID)
	}
	a.history = resp.Run.Messages
	a.runID = runID
	return nil
}

// Fork copies the current run's log into a new run (newRunID empty asks
// the store to generate one) and switches the session to the copy: the
// in-memory history is already the shared prefix, so the fork diverges
// from the next turn on while the original run stays untouched.
func (a *App) Fork(ctx context.Context, newRunID string) (string, error) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.store == nil {
		return "", fmt.Errorf("host: no RunStore configured")
	}
	if a.runID == "" {
		return "", fmt.Errorf("host: no active run to fork (run at least one turn first)")
	}
	resp, err := a.store.ForkRun(ctx, agent.ForkRunRequest{RunID: a.runID, NewRunID: newRunID})
	if err != nil {
		return "", fmt.Errorf("host: forking run %q: %w", a.runID, err)
	}
	if !resp.Found {
		return "", fmt.Errorf("host: run %q not found", a.runID)
	}
	if !resp.Created {
		return "", fmt.Errorf("host: run %q already exists", newRunID)
	}
	a.runID = resp.RunID
	return resp.RunID, nil
}

// ensureRunLocked lazily creates the session's run before the first
// persisted turn. Caller holds turnMu.
func (a *App) ensureRunLocked(ctx context.Context) error {
	if a.runID != "" {
		return nil
	}
	resp, err := a.store.CreateRun(ctx, agent.CreateRunRequest{})
	if err != nil {
		return fmt.Errorf("host: creating run: %w", err)
	}
	a.runID = resp.RunID
	a.renderer.session(resp.RunID)
	return nil
}

// persistTurnLocked appends the completed turn (user message + appended
// messages) and flushes the teed event stream. Persistence failures are
// rendered as warnings, not turn failures: the turn already succeeded
// and the in-memory session stays usable; only durability degraded.
// Caller holds turnMu.
func (a *App) persistTurnLocked(ctx context.Context, msgs []agent.Message, pe *PersistingEmit) {
	resp, err := a.store.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: a.runID, Messages: msgs})
	if err != nil {
		a.renderer.sessionWarn(err)
	} else if !resp.Found {
		a.renderer.sessionWarn(fmt.Errorf("run %q disappeared from the store", a.runID))
	}
	if err := pe.Flush(ctx); err != nil {
		a.renderer.sessionWarn(err)
	}
}

// PersistingEmit tees a Runner event stream into a RunStore event log.
// Use its Emit as the emit argument to Runner.Run (wrapping the
// surface's own handler), then Flush once the turn completes. Events
// buffer in memory and land in one AppendEvents call per turn — the
// per-turn durability grain — rather than one store round-trip per
// text delta. Not safe for concurrent use, which matches the Runner's
// guarantee that emit is never called concurrently.
type PersistingEmit struct {
	store agent.RunStore
	runID string
	next  func(agent.Event)
	buf   []agent.Event
}

// NewPersistingEmit wraps next (nil is allowed) for the given run.
func NewPersistingEmit(store agent.RunStore, runID string, next func(agent.Event)) *PersistingEmit {
	if next == nil {
		next = func(agent.Event) {}
	}
	return &PersistingEmit{store: store, runID: runID, next: next}
}

// Emit buffers the event and forwards it to the wrapped handler.
func (p *PersistingEmit) Emit(e agent.Event) {
	p.buf = append(p.buf, e)
	p.next(e)
}

// Flush appends the buffered events to the run and clears the buffer.
// A run the store does not know is reported as an error here (unlike
// the raw AppendEvents Found=false) because by construction the caller
// created the run before the turn began.
func (p *PersistingEmit) Flush(ctx context.Context) error {
	if len(p.buf) == 0 {
		return nil
	}
	resp, err := p.store.AppendEvents(ctx, agent.AppendEventsRequest{RunID: p.runID, Events: p.buf})
	if err != nil {
		return err
	}
	if !resp.Found {
		return fmt.Errorf("host: run %q disappeared from the store", p.runID)
	}
	p.buf = nil
	return nil
}
