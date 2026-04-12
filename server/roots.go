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
	"strings"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// defaultRootsFetchTimeout is the deadline for server-to-client roots/list
// requests when WithRootsFetchTimeout is not set. 30 seconds covers common
// use cases (local filesystems, small monorepos) without being so long that
// a stuck client blocks server startup perceptibly.
const defaultRootsFetchTimeout = 30 * time.Second

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

	timeout := d.rootsFetchTimeout
	if timeout <= 0 {
		timeout = defaultRootsFetchTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
	// either side don't leak. make()+copy ensures the result is non-nil
	// even when the client returned an empty roots list (append(nil) is nil).
	stored := make([]core.Root, len(result.Roots))
	copy(stored, result.Roots)

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
		cbCopy := make([]core.Root, len(stored))
		copy(cbCopy, stored)
		d.onRootsChanged(cbCopy)
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

// effectiveAllowedRoots computes the enforced filesystem roots for the
// current session. The result depends on two inputs:
//
//   - d.allowedRoots: static list from WithAllowedRoots (may be nil/empty).
//   - d.roots: dynamic client-provided roots from roots/list (may be nil).
//
// Rules:
//   - If both are set: intersection (paths must appear in BOTH lists).
//   - If only static is set: use the static list.
//   - If only client roots are set: convert file:// URIs to paths, use those.
//   - If neither is set: return nil (no restriction).
//
// Thread-safe: reads d.roots under rootsMu.
func (d *Dispatcher) effectiveAllowedRoots() []string {
	d.rootsMu.Lock()
	clientRoots := d.roots
	d.rootsMu.Unlock()

	hasStatic := len(d.allowedRoots) > 0
	hasClient := len(clientRoots) > 0

	if !hasStatic && !hasClient {
		return nil
	}

	if !hasStatic {
		// Client roots only — convert URIs to paths.
		paths := make([]string, len(clientRoots))
		for i, r := range clientRoots {
			paths[i] = core.FileURIToPath(r.URI)
		}
		return paths
	}

	if !hasClient {
		// Static roots only.
		return append([]string(nil), d.allowedRoots...)
	}

	// Intersection: a client root must fall within at least one static root.
	// Convert client URIs to paths, then filter by static containment.
	staticSet := make(map[string]bool, len(d.allowedRoots))
	for _, s := range d.allowedRoots {
		staticSet[s] = true
	}
	var result []string
	for _, cr := range clientRoots {
		p := core.FileURIToPath(cr.URI)
		for _, s := range d.allowedRoots {
			if p == s || strings.HasPrefix(p, s+"/") || strings.HasPrefix(s, p+"/") || staticSet[p] {
				result = append(result, p)
				break
			}
		}
	}
	if result == nil {
		// Explicit empty: intersection produced nothing → deny all.
		return []string{}
	}
	return result
}
