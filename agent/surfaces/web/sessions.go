package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

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
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*host.App
	factory  func(context.Context) (*host.App, error)
}

// NewSessionManager returns a manager whose Create builds fresh Apps from
// factory. factory may be nil, in which case the manager can only serve a
// default session registered via SetDefault and Create returns
// ErrNoSessionFactory.
func NewSessionManager(factory func(context.Context) (*host.App, error)) *SessionManager {
	return &SessionManager{sessions: map[string]*host.App{}, factory: factory}
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
func (m *SessionManager) Create(ctx context.Context) (string, *host.App, error) {
	if m.factory == nil {
		return "", nil, ErrNoSessionFactory
	}
	app, err := m.factory(ctx)
	if err != nil {
		return "", nil, err
	}
	id := m.mintID()
	m.mu.Lock()
	m.sessions[id] = app
	m.mu.Unlock()
	return id, app, nil
}

// Get returns the App for id and whether it was found. An empty id resolves to
// the default session. A missing session is app state (found=false), not an
// error, so the caller maps it to a Connect CodeNotFound.
func (m *SessionManager) Get(id string) (*host.App, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	app, ok := m.sessions[resolve(id)]
	return app, ok
}

// List returns the ids of every live session, unordered. The default session's
// id is included.
func (m *SessionManager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// Close closes the App for id and drops it from the roster, returning whether a
// session was found. An empty id resolves to the default session. Closing is
// idempotent: a second Close of the same id returns false.
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
