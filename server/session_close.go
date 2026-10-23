package server

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
)

// SessionCloseHook is called once when a session ends. sessionID is the
// value the transport assigned: the Mcp-Session-Id on Streamable HTTP and
// SSE, "stdio" for the stdio transport, "memory" for the in-process one.
type SessionCloseHook func(ctx context.Context, sessionID string)

// sessionCloseHooks is shared by the server's root dispatcher and every
// session dispatcher cloned from it, the way subManager is, so a hook
// registered after sessions exist still fires for them.
type sessionCloseHooks struct {
	mu  sync.RWMutex
	fns []SessionCloseHook
}

func (h *sessionCloseHooks) add(fn SessionCloseHook) {
	h.mu.Lock()
	h.fns = append(h.fns, fn)
	h.mu.Unlock()
}

func (h *sessionCloseHooks) fire(sessionID string) {
	if h == nil {
		return
	}
	h.mu.RLock()
	fns := append([]SessionCloseHook(nil), h.fns...)
	h.mu.RUnlock()
	for _, fn := range fns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("mcpkit: session close hook panicked", "session", sessionID, "panic", r, "stack", string(debug.Stack()))
				}
			}()
			fn(context.Background(), sessionID)
		}()
	}
}

// OnSessionClose registers fn to run when a session ends, so an extension
// holding per-session state (attachments, pairings, caches) can drop it. It
// fires exactly once per session, whichever way the session ends: a client
// DELETE, idle expiry, the SSE stream closing, Server shutdown, or the stdio
// or in-process transport closing.
//
// Hooks run synchronously, in registration order, on the goroutine tearing
// the session down, after the session's subscriptions are released. Keep them
// short; anything slow belongs on a goroutine of its own. The ctx carries no
// request, since none is in flight. A panicking hook is logged and the rest
// still run.
//
// The SEP-2575 stateless wire has no sessions, so nothing fires for its
// requests. Safe to call at any time, including while sessions are live.
func (s *Server) OnSessionClose(fn SessionCloseHook) {
	if fn == nil {
		return
	}
	s.dispatcher.closeHooks.add(fn)
}
