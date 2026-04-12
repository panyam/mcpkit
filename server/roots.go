package server

// Roots-list fetch implementation (#26).
//
// When the client sends notifications/roots/list_changed, the server issues
// a server-to-client roots/list request, stores the result in d.roots, and
// invokes the WithOnRootsChanged callback with the populated list. The fetch
// runs on a dedicated goroutine so notifications never block the POST that
// delivered them — blocking would deadlock HTTP transports where the client
// may serialize its own POSTs.
//
// Concurrency model:
//
//   rootsMu guards roots, rootsStale, and rootsFetching. It is never held
//   across the outbound RPC or the user callback. A concurrent burst of
//   list_changed notifications collapses to at most one in-flight fetch +
//   one coalesced re-fetch via rootsStale. See refreshRoots.

import (
	"context"
	"encoding/json"
	"log"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// rootsFetchTimeout bounds the duration of a single server-to-client
// roots/list request. Hardcoded for now; if we need tunability we'll
// promote it to a WithRootsFetchTimeout server option.
const rootsFetchTimeout = 30 * time.Second

// handleRootsListChanged is the entry point for the
// notifications/roots/list_changed notification. It gates on the client's
// declared capability, grabs the transport's persistent pushRequest, and
// spawns refreshRoots on a goroutine so the notification-delivery POST
// returns immediately.
func (d *Dispatcher) handleRootsListChanged() {
	// Capability gate: only clients that declared roots.listChanged support
	// the roots/list method. Silently skip for clients that didn't opt in.
	if d.clientCaps.Roots == nil || !d.clientCaps.Roots.ListChanged {
		return
	}

	push := d.getPushRequest()
	if push == nil {
		// Transport has no persistent server-initiated request capability
		// (e.g., Streamable HTTP before the GET SSE stream has been opened).
		// Mark stale so if a push function is wired later, a future
		// invocation can observe the staleness. Future work: auto-fetch on
		// the first tool call once a push function is available.
		d.rootsMu.Lock()
		d.rootsStale = true
		d.rootsMu.Unlock()
		return
	}

	// Dedup: if a fetch is already in flight, just mark stale and return —
	// the in-flight goroutine's defer block will re-dispatch a follow-up
	// fetch when it sees the stale flag.
	d.rootsMu.Lock()
	if d.rootsFetching {
		d.rootsStale = true
		d.rootsMu.Unlock()
		return
	}
	d.rootsFetching = true
	d.rootsStale = false
	d.rootsMu.Unlock()

	go d.refreshRoots(push)
}

// refreshRoots issues a single server-to-client roots/list request, stores
// the result, invokes the onRootsChanged callback, and — if another
// list_changed notification arrived during the fetch — re-dispatches
// itself. Runs on its own goroutine.
func (d *Dispatcher) refreshRoots(push func(json.RawMessage)) {
	defer func() {
		// Post-fetch: drop the fetching flag and, if a concurrent notification
		// invalidated the result mid-flight, spawn a follow-up fetch to
		// converge on the latest state. The follow-up runs as a fresh
		// goroutine (not a loop) so the dedup guard sequencing is obvious:
		// rootsFetching is false before the next fetch starts, so a racing
		// handleRootsListChanged either wins the CAS (dispatches the
		// follow-up itself) or observes rootsFetching and marks stale again.
		d.rootsMu.Lock()
		d.rootsFetching = false
		restale := d.rootsStale
		d.rootsMu.Unlock()
		if restale {
			// Re-enter handleRootsListChanged so capability + push-function
			// gates are re-checked (both can change mid-session).
			d.handleRootsListChanged()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), rootsFetchTimeout)
	defer cancel()

	reqFunc := d.makeRequestFunc(push)
	raw, err := reqFunc(ctx, "roots/list", nil)
	if err != nil {
		// Don't invoke the callback with a garbage/empty list — callers
		// cannot distinguish "no roots" from "fetch failed" if we do.
		log.Printf("mcpkit: roots/list fetch failed: %v", err)
		return
	}

	var result core.RootsListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		log.Printf("mcpkit: roots/list response decode failed: %v", err)
		return
	}

	// Defensive copy: decouple the slice we expose to d.roots / callbacks
	// from the decoded response's backing array so later mutations on
	// either side don't leak.
	stored := append([]core.Root(nil), result.Roots...)

	d.rootsMu.Lock()
	d.roots = stored
	d.rootsMu.Unlock()

	// Invoke the callback OUTSIDE the lock — callbacks may acquire locks
	// of their own or issue long-running work and must never run under
	// rootsMu (rule enforced by the closeness of this file alone; no code
	// path outside refreshRoots mutates d.roots today).
	if d.onRootsChanged != nil {
		// Pass a fresh copy so a misbehaving callback cannot corrupt the
		// stored slice via append/mutation.
		d.onRootsChanged(append([]core.Root(nil), stored...))
	}
}

// Roots returns a snapshot of the client's most recently reported roots.
// The returned slice is safe to retain and mutate — it is a fresh copy.
// Returns nil if no roots/list fetch has completed yet (the typical state
// until the client first sends notifications/roots/list_changed).
func (d *Dispatcher) Roots() []core.Root {
	d.rootsMu.Lock()
	defer d.rootsMu.Unlock()
	if d.roots == nil {
		return nil
	}
	return append([]core.Root(nil), d.roots...)
}
