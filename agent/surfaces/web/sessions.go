package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
)

// DefaultSessionID is the id of the session an empty session_id routes to. One
// agentweb server always has a default session so a client that predates
// multi-session (and sends no session_id) keeps working unchanged.
const DefaultSessionID = "default"

// ErrNoSessionFactory is returned by SessionManager.Create when the manager was
// built without a factory, so it can only serve its pre-built default session
// (the single-App back-compat path).
var ErrNoSessionFactory = errors.New("web: session manager has no factory; cannot create sessions")

// SessionManager owns the live host.Apps a single agentweb server hosts, one
// per concurrent conversation, keyed by session id. It is safe for concurrent
// use. A request's session_id selects its App via Get; an empty session_id
// resolves to the default session. New sessions are built on demand from a
// factory that produces a fresh App from the server's config.
//
// When a store is configured (NewSessionManagerWithStore), the manager is a
// live-App cache over a durable RunStore roster: session_id is the run id, a
// Create attaches a fresh run, and a Get for a session that is not cached (a
// process restart dropped the live App) rehydrates it from the store by
// resuming its run. The factory must build Apps with the same store attached
// (host.WithRunStore) so a rehydrated App keeps persisting. With no store the
// manager is a plain in-memory cache: nothing survives a restart, matching the
// original behavior.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*host.App
	factory  func(context.Context) (*host.App, error)
	// store, when non-nil, is the durable roster the manager caches live Apps
	// over. It is the SAME store the factory attaches to each App, so a Create
	// attaches a run under the session id and a Get rehydrates a dropped session
	// by resuming that run. Nil keeps the pure in-memory behavior.
	store agent.RunStore
}

// NewSessionManager returns a manager whose Create builds fresh Apps from
// factory. factory may be nil, in which case the manager can only serve a
// default session registered via SetDefault and Create returns
// ErrNoSessionFactory. The manager has no durable store: sessions live only in
// memory and do not survive a process restart (use NewSessionManagerWithStore
// for persistence).
func NewSessionManager(factory func(context.Context) (*host.App, error)) *SessionManager {
	return &SessionManager{sessions: map[string]*host.App{}, factory: factory}
}

// NewSessionManagerWithStore returns a manager that caches live Apps over the
// durable RunStore store: session_id is a run id, Create attaches a run under a
// freshly minted id, and Get rehydrates a session that is not cached (e.g. after
// a restart) by resuming its run — so sessions survive a process restart. store
// must be the SAME RunStore the factory attaches to every App it builds (via
// host.WithRunStore), or a rehydrated App will not persist. A nil store is
// equivalent to NewSessionManager.
func NewSessionManagerWithStore(factory func(context.Context) (*host.App, error), store agent.RunStore) *SessionManager {
	return &SessionManager{sessions: map[string]*host.App{}, factory: factory, store: store}
}

// resolve maps an empty id to the default session id, so an empty session_id on
// the wire always addresses the default session.
func resolve(id string) string {
	if id == "" {
		return DefaultSessionID
	}
	return id
}

// SetDefault registers app as the default session (the one an empty session_id
// routes to). It is called once at startup; a prior default is not closed.
func (m *SessionManager) SetDefault(app *host.App) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[DefaultSessionID] = app
}

// Create builds a fresh App via the factory, stores it under a newly minted id,
// and returns the id with the App. It returns ErrNoSessionFactory when the
// manager has no factory. A factory error is propagated and nothing is stored.
//
// With a store configured, the minted id is also the run id: Create attaches a
// fresh run to the App (host.App.AttachRun), so the session is on the durable
// roster and survives a restart. An AttachRun error is propagated and nothing is
// stored. Without a store the id is just an in-memory key and no run is created.
func (m *SessionManager) Create(ctx context.Context) (string, *host.App, error) {
	if m.factory == nil {
		return "", nil, ErrNoSessionFactory
	}
	app, err := m.factory(ctx)
	if err != nil {
		return "", nil, err
	}
	id := m.mintID()
	if m.store != nil {
		// session_id == run id: attach (create) the run so it lands on the
		// durable roster and a later Get can rehydrate it. AttachRun is
		// create-or-resume, but the minted id is unique so this always creates.
		if err := app.AttachRun(ctx, id); err != nil {
			app.Close()
			return "", nil, err
		}
	}
	m.mu.Lock()
	m.sessions[id] = app
	m.mu.Unlock()
	return id, app, nil
}

// Get returns the App for id and whether it was found. An empty id resolves to
// the default session. A missing session is app state (found=false), not an
// error, so the caller maps it to a Connect CodeNotFound.
//
// With a store configured, a session that is not cached (e.g. dropped by a
// process restart) is rehydrated: Get builds a fresh App via the factory and
// resumes the run whose id is this session id. A run the store does not know
// resolves to found=false (still app state, not an error). ctx threads the
// store round-trips (LoadRun) the rehydration performs; the App build and
// resume run outside the manager lock so a slow factory does not serialize
// concurrent session access, and a lost race closes the redundant App.
func (m *SessionManager) Get(ctx context.Context, id string) (*host.App, bool) {
	rid := resolve(id)
	m.mu.Lock()
	app, ok := m.sessions[rid]
	m.mu.Unlock()
	if ok {
		return app, true
	}
	if m.store == nil || m.factory == nil {
		return nil, false
	}
	app, err := m.factory(ctx)
	if err != nil {
		return nil, false
	}
	if err := app.Resume(ctx, rid); err != nil {
		// Unknown run (or a load failure) is not-found — the caller maps it to a
		// Connect CodeNotFound, exactly as a never-created session does.
		app.Close()
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// A concurrent Get may have rehydrated the same session; keep the first
	// winner and drop ours so only one live App backs a session.
	if existing, ok := m.sessions[rid]; ok {
		app.Close()
		return existing, true
	}
	m.sessions[rid] = app
	return app, true
}

// List returns the ids of every session, unordered. With a store configured it
// returns the durable roster (RunStore.ListRuns, every persisted run id, so a
// restart still shows past sessions) merged with any live-only ids the store
// does not yet know; without a store it returns the in-memory keys. The default
// session's id is included. ctx threads the store round-trips.
func (m *SessionManager) List(ctx context.Context) []string {
	if m.store == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		ids := make([]string, 0, len(m.sessions))
		for id := range m.sessions {
			ids = append(ids, id)
		}
		return ids
	}
	// The durable roster is the source of truth for persisted sessions; page
	// through every run so a restart (empty live map) still lists them.
	seen := map[string]bool{}
	var ids []string
	cursor := ""
	for {
		resp, err := m.store.ListRuns(ctx, agent.ListRunsRequest{Cursor: cursor})
		if err != nil {
			break
		}
		for _, r := range resp.Runs {
			if !seen[r.ID] {
				seen[r.ID] = true
				ids = append(ids, r.ID)
			}
		}
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	// Merge any live-only ids the store does not know (a session cached but whose
	// run is not yet persisted). With the current factory-attaches-the-store
	// wiring this is normally empty, but the merge keeps List honest if it isn't.
	m.mu.Lock()
	for id := range m.sessions {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	return ids
}

// Close closes the App for id and drops it from the live cache, returning
// whether a live session was found. An empty id resolves to the default session.
// Closing is idempotent: a second Close of the same id returns false. With a
// store configured the run is NOT deleted from the store — only the live App is
// evicted, so a later Get rehydrates the session from its persisted run.
func (m *SessionManager) Close(id string) bool {
	rid := resolve(id)
	m.mu.Lock()
	app, ok := m.sessions[rid]
	if ok {
		delete(m.sessions, rid)
	}
	m.mu.Unlock()
	if ok {
		app.Close()
	}
	return ok
}

// CloseAll closes every live session and empties the roster. It is used to tear
// the whole server down.
func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	apps := make([]*host.App, 0, len(m.sessions))
	for id, app := range m.sessions {
		apps = append(apps, app)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, app := range apps {
		app.Close()
	}
}

// mintID returns a fresh, unpredictable session id (crypto/rand hex). It never
// collides with DefaultSessionID and, being 128 bits, never collides in
// practice with a live session.
func (m *SessionManager) mintID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
