package host

import (
	"context"
	"fmt"
	"strings"

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

// WithToolResultStore supplies the backing store for tool-result
// offloading (Config.Offload). Omitted, offloading uses an in-memory
// store; pass a durable one (agent/store/redis, agent/store/gorm) so
// offloaded blobs survive restarts. Ignored when Config.Offload is nil.
func WithToolResultStore(store agent.ToolResultStore) AppOption {
	return func(o *appOptions) { o.toolResultStore = store }
}

// WithProviderBuilder overrides how Config.Connections entries are turned
// into providers. Tests inject a builder returning StubProviders;
// production uses DefaultProviderBuilder (OpenAI-compatible). Ignored when
// Connections is nil or WithProvider is set.
func WithProviderBuilder(b ProviderBuilder) AppOption {
	return func(o *appOptions) { o.providerBuilder = b }
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

// Sessions lists the first page of persisted runs newest-first (the
// /sessions picker), or an error when no RunStore is configured. It is the
// first-page shim over SessionsPage; a surface that pages calls SessionsPage.
func (a *App) Sessions(ctx context.Context) ([]agent.RunInfo, error) {
	page, err := a.SessionsPage(ctx, "")
	if err != nil {
		return nil, err
	}
	return page.Runs, nil
}

// SessionPage is one page of the session picker: the runs on this page and
// HasMore reporting whether another page exists (the underlying cursor is
// remembered on the App so the next SessionsPage("") advance-from-last works
// via the /sessions "more" flow — callers page by empty cursor for the first
// page, then let the host thread its remembered cursor).
type SessionPage struct {
	Runs    []agent.RunInfo
	HasMore bool
}

// SessionsPage returns one page of runs newest-first (gorm / in-memory order
// recency for free; redis SCAN is unordered — see the runstore docs). An empty
// cursor starts from the most recent; any other value continues a prior page.
// The response's next cursor is remembered on the App so PageMore advances
// without the surface handling the opaque cursor. Errors when no RunStore is
// configured.
func (a *App) SessionsPage(ctx context.Context, cursor string) (SessionPage, error) {
	if a.store == nil {
		return SessionPage{}, fmt.Errorf("host: no RunStore configured")
	}
	resp, err := a.store.ListRuns(ctx, agent.ListRunsRequest{Cursor: cursor})
	if err != nil {
		return SessionPage{}, fmt.Errorf("host: listing sessions: %w", err)
	}
	a.sessionsMu.Lock()
	a.sessionsCursor = resp.NextCursor
	a.sessionsMu.Unlock()
	return SessionPage{Runs: resp.Runs, HasMore: resp.NextCursor != ""}, nil
}

// PageMore returns the next page after the last SessionsPage call (the
// /sessions "more" flow), or an empty page when there is nothing more.
func (a *App) PageMore(ctx context.Context) (SessionPage, error) {
	a.sessionsMu.Lock()
	cursor := a.sessionsCursor
	a.sessionsMu.Unlock()
	if cursor == "" {
		return SessionPage{}, nil
	}
	return a.SessionsPage(ctx, cursor)
}

// maxSessionSearchScan bounds SearchSessions: id-substring search has no
// store-side filter, so the host walks pages — capped so a huge run set can't
// turn a search into an unbounded scan. Content search is a future index.
const maxSessionSearchScan = 1000

// SearchSessions returns runs whose ID contains query (case-insensitive),
// newest-first, walking pages up to maxSessionSearchScan runs. The scan is
// bounded, so a match past the cap is not found — a deliberate limit until a
// real search index exists.
func (a *App) SearchSessions(ctx context.Context, query string) ([]agent.RunInfo, error) {
	if a.store == nil {
		return nil, fmt.Errorf("host: no RunStore configured")
	}
	q := strings.ToLower(query)
	var out []agent.RunInfo
	cursor := ""
	scanned := 0
	for scanned < maxSessionSearchScan {
		resp, err := a.store.ListRuns(ctx, agent.ListRunsRequest{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("host: searching sessions: %w", err)
		}
		for _, r := range resp.Runs {
			scanned++
			if strings.Contains(strings.ToLower(r.ID), q) {
				out = append(out, r)
			}
		}
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	return out, nil
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
// the store to generate one) and switches the session to the copy,
// which diverges from the next turn on while the original stays
// untouched. atMessage positive forks from an earlier point — only the
// first atMessage messages carry over (checkpoint/rewind), and the
// in-memory history rewinds to match by reloading the fork; zero or
// negative forks the whole log, and history is already the shared
// prefix so no reload is needed.
func (a *App) Fork(ctx context.Context, newRunID string, atMessage int) (string, error) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.store == nil {
		return "", fmt.Errorf("host: no RunStore configured")
	}
	if a.runID == "" {
		return "", fmt.Errorf("host: no active run to fork (run at least one turn first)")
	}
	resp, err := a.store.ForkRun(ctx, agent.ForkRunRequest{RunID: a.runID, NewRunID: newRunID, AtMessage: atMessage})
	if err != nil {
		return "", fmt.Errorf("host: forking run %q: %w", a.runID, err)
	}
	if !resp.Found {
		return "", fmt.Errorf("host: run %q not found", a.runID)
	}
	if !resp.Created {
		return "", fmt.Errorf("host: run %q already exists", newRunID)
	}
	if atMessage > 0 {
		return resp.RunID, a.resumeLocked(ctx, resp.RunID)
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
	a.emit(HostEvent{Kind: HostSessionChanged, RunID: resp.RunID})
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
		a.emit(HostEvent{Kind: HostSessionWarn, Err: err.Error()})
	} else if !resp.Found {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("run %q disappeared from the store", a.runID)})
	}
	if err := pe.Flush(ctx); err != nil {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: err.Error()})
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

// Emit forwards the event to the wrapped handler verbatim and buffers a
// persistence copy. The two differ only for turn-end: the buffered copy
// drops TurnResult.Messages (see redactForPersist), so live consumers
// (renderers) still see the whole event while the event log does not
// duplicate what the message log already holds.
func (p *PersistingEmit) Emit(e agent.Event) {
	p.buf = append(p.buf, redactForPersist(e))
	p.next(e)
}

// redactForPersist strips TurnResult.Messages from a turn-end event
// before it lands in the persisted event log. The RunStore message log
// is the authoritative copy of those messages; re-storing them inside
// the turn-end event doubles the payload (and, with large tool results,
// the offloaded stubs). Everything else on the TurnResult — Text, Usage,
// Steps, FinishReason, Structured — stays for replay and audit. Non
// turn-end events pass through unchanged. The original event and its
// TurnResult are never mutated: the copy is shallow with Messages
// nilled, so the live handler's view is untouched.
func redactForPersist(e agent.Event) agent.Event {
	if e.Kind != agent.EventTurnEnd || e.Result == nil {
		return e
	}
	trimmed := *e.Result
	trimmed.Messages = nil
	e.Result = &trimmed
	return e
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
