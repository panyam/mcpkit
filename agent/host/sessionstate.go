package host

import (
	"sync"
	"sync/atomic"

	"github.com/panyam/mcpkit/agent"
)

// sessionState is the persistence half of a session: where runs are stored,
// which run is active, and where the /sessions picker last paged to.
//
// Locking here is deliberately not uniform, because the state is not.
//
//   - store is set once at construction and read-only afterwards.
//   - runID is guarded by the App's turnMu, not by anything here. It changes
//     only at points a turn already owns (attach, resume, fork, lazy create),
//     so giving it a second lock would add a hierarchy without removing a
//     race. runIDAtomic mirrors it for readers that cannot take turnMu.
//   - cursor has its own mutex because paging is a UI action unrelated to a
//     turn and can arrive while one is running.
//
// Held by value on App rather than by pointer, because every field is
// zero-value usable: a nil store is "persistence off", and the rest are a
// string, an atomic, and a mutex. That keeps a partially-constructed App
// (which the tests build freely) working instead of panicking on a nil
// cluster — the failure mode a pointer would introduce for no gain. Never
// copy it; the mutex and atomic make a copy meaningless.
type sessionState struct {
	store agent.RunStore

	// runID is the run turns append to. Guarded by App.turnMu; write through
	// setRunID so the atomic mirror stays in step.
	runID string

	// runIDAtomic mirrors runID for a lock-free read. The session-scoped
	// memory namespace func reads it during a turn while turnMu is already
	// held, so it must not go through anything that takes turnMu.
	runIDAtomic atomic.Value

	cursorMu sync.Mutex
	cursor   string
}

// setRunID updates the active run id and its lock-free mirror together.
// Caller holds turnMu (every write site does). Routing all four writers
// (AttachRun, resumeLocked, Fork, ensureRunLocked) through here keeps the
// mirror from drifting out of sync.
func (s *sessionState) setRunID(id string) {
	s.runID = id
	s.runIDAtomic.Store(id)
}

// currentRunID reads the active run id WITHOUT taking turnMu, so it is safe
// to call from inside a turn (the memory namespace func runs while RunTurn
// already holds turnMu — a locking read would deadlock there). Returns ""
// before any run is set, which the session-scoped namespace func maps to the
// shared default scratchpad.
func (s *sessionState) currentRunID() string {
	if v := s.runIDAtomic.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// rememberCursor records where the last /sessions page ended, so "more" can
// advance. The store's cursor is opaque, which is why the host holds it.
func (s *sessionState) rememberCursor(c string) {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	s.cursor = c
}

// nextCursor returns the paging position, empty when there is no more.
func (s *sessionState) nextCursor() string {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	return s.cursor
}
