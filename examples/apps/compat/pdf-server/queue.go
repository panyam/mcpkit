package main

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/panyam/gocurrent"
)

// Per-viewUUID state machinery backing the --enable-interact surface.
//
// Three request/response patterns ride through this hub:
//
//  1. interact (model → viewer): commands queue up per viewUUID.
//     poll_pdf_commands long-polls; the queue drains atomically on each
//     wake-up so the viewer sees a coherent batch.
//
//  2. get_pages / save_as (server → viewer): server enqueues a command
//     carrying a requestId, then blocks waiting for submit_page_data /
//     submit_save_data to call back with the result.
//
//  3. get_viewer_state (server → viewer): same shape as #2 for the
//     viewer's live state snapshot (currentPage, zoom, selection, ...).
//
// Long-poll waiter pattern lifted from upstream: at most one poll waits
// per viewUUID. A new poll wakes the old one so the slot stays single-
// occupancy.

// Poll-timing constants match upstream so the viewer's batching window
// stays the same regardless of which server it talks to.
const (
	pollBatchWaitMs    = 200
	longPollTimeoutMs  = 30_000
	pendingRequestWait = 60 * time.Second
)

// pdfCommand is the wire shape the viewer expects on a poll_pdf_commands
// response. Map shape mirrors upstream's `InteractCommand` schema —
// we don't introspect it server-side beyond the requestId.
type pdfCommand map[string]any

// pendingChan is a single-use rendezvous resolved by a submit_*
// callback. payload is the JSON-encoded result; err is a non-empty
// string when the viewer reported failure. once guards both writers
// (submit_*) and the timeout cleanup path.
type pendingChan struct {
	ch   chan pendingResult
	once sync.Once
}

type pendingResult struct {
	payload string
	err     string
}

func newPendingChan() *pendingChan {
	return &pendingChan{ch: make(chan pendingResult, 1)}
}

func (p *pendingChan) resolve(payload, err string) {
	p.once.Do(func() {
		p.ch <- pendingResult{payload: payload, err: err}
		close(p.ch)
	})
}

// viewState is the per-viewUUID command queue. cmds is drained
// atomically on each poll; waiter is the single-slot long-poll
// wake-up channel.
type viewState struct {
	mu     sync.Mutex
	cmds   []pdfCommand
	waiter chan struct{} // single-slot; new pollers replace and close the old one
}

// hub holds all live state for the process. typed concurrent maps via
// gocurrent.SyncMap so we don't carry sync.Map's interface-typed Get
// + cast through every call site.
type hub struct {
	views         gocurrent.SyncMap[string, *viewState]
	pendingPages  gocurrent.SyncMap[string, *pendingChan]
	pendingSaves  gocurrent.SyncMap[string, *pendingChan]
	pendingStates gocurrent.SyncMap[string, *pendingChan]
}

func newHub() *hub {
	return &hub{}
}

// viewFor returns the per-viewUUID state, creating it on first use.
// Cheap to call from any code path that needs queue or waiter access.
func (h *hub) viewFor(viewUUID string) *viewState {
	if v, ok := h.views.Load(viewUUID); ok {
		return v
	}
	v, _ := h.views.LoadOrStore(viewUUID, &viewState{})
	return v
}

// enqueueCommand appends to the per-UUID queue and wakes any in-flight
// long-poll. Safe under concurrent enqueue.
func (h *hub) enqueueCommand(viewUUID string, cmd pdfCommand) {
	v := h.viewFor(viewUUID)
	v.mu.Lock()
	v.cmds = append(v.cmds, cmd)
	waiter := v.waiter
	v.waiter = nil
	v.mu.Unlock()
	if waiter != nil {
		close(waiter)
	}
}

// dequeueCommands drains and returns the queued commands. Empty slice
// (never nil) so the JSON response always carries a real array.
func (h *hub) dequeueCommands(viewUUID string) []pdfCommand {
	v := h.viewFor(viewUUID)
	v.mu.Lock()
	cmds := v.cmds
	v.cmds = nil
	v.mu.Unlock()
	if cmds == nil {
		cmds = []pdfCommand{}
	}
	return cmds
}

// longPoll implements the poll_pdf_commands wait loop:
//
//   - If commands are already queued, sit for a short batch window so
//     subsequent enqueues join the same wake-up, then drain.
//   - Otherwise install (or replace) the single-slot waiter and block
//     until commands arrive, the long-poll timeout fires, or the
//     request context cancels.
//
// Returns the drained command list on resume.
func (h *hub) longPoll(ctx context.Context, viewUUID string) []pdfCommand {
	v := h.viewFor(viewUUID)

	v.mu.Lock()
	hasQueued := len(v.cmds) > 0
	v.mu.Unlock()

	if hasQueued {
		select {
		case <-time.After(time.Duration(pollBatchWaitMs) * time.Millisecond):
		case <-ctx.Done():
		}
		return h.dequeueCommands(viewUUID)
	}

	// Install a new waiter, kicking out any stale one.
	waiter := make(chan struct{})
	v.mu.Lock()
	if prev := v.waiter; prev != nil {
		close(prev)
	}
	v.waiter = waiter
	v.mu.Unlock()

	timer := time.NewTimer(time.Duration(longPollTimeoutMs) * time.Millisecond)
	defer timer.Stop()

	select {
	case <-waiter:
	case <-timer.C:
	case <-ctx.Done():
	}

	// Clean the slot if it's still ours (enqueueCommand nils it when it wakes us).
	v.mu.Lock()
	if v.waiter == waiter {
		v.waiter = nil
	}
	hasQueued = len(v.cmds) > 0
	v.mu.Unlock()

	// Batch window if any commands raced in.
	if hasQueued {
		select {
		case <-time.After(time.Duration(pollBatchWaitMs) * time.Millisecond):
		case <-ctx.Done():
		}
	}
	return h.dequeueCommands(viewUUID)
}

// awaitPage / awaitSave / awaitState register a server-side waiter for
// a submit_* callback. The caller enqueues the matching command via
// enqueueCommand, then blocks here for the viewer's reply.
//
// pendingRequestWait caps the wait so a misbehaving or disconnected
// viewer can't pin a goroutine forever.

func (h *hub) awaitPage(ctx context.Context, requestID string) pendingResult {
	return h.await(ctx, requestID, &h.pendingPages)
}

func (h *hub) awaitSave(ctx context.Context, requestID string) pendingResult {
	return h.await(ctx, requestID, &h.pendingSaves)
}

func (h *hub) awaitState(ctx context.Context, requestID string) pendingResult {
	return h.await(ctx, requestID, &h.pendingStates)
}

func (h *hub) await(ctx context.Context, requestID string, m *gocurrent.SyncMap[string, *pendingChan]) pendingResult {
	pending := newPendingChan()
	m.Store(requestID, pending)
	defer m.Delete(requestID)

	select {
	case res := <-pending.ch:
		return res
	case <-time.After(pendingRequestWait):
		return pendingResult{err: "request timed out waiting for viewer response"}
	case <-ctx.Done():
		return pendingResult{err: "request canceled"}
	}
}

// resolvePending wakes a server-side awaiter. Returns false if no waiter
// is registered — that's a viewer bug (submit_* with an unknown
// requestId), so the handler surfaces it as isError.
func (h *hub) resolvePending(kind, requestID, payload, errMsg string) bool {
	var pending *pendingChan
	var ok bool
	switch kind {
	case "page":
		pending, ok = h.pendingPages.Load(requestID)
	case "save":
		pending, ok = h.pendingSaves.Load(requestID)
	case "state":
		pending, ok = h.pendingStates.Load(requestID)
	}
	if !ok {
		return false
	}
	pending.resolve(payload, errMsg)
	return true
}

// marshalCommands serializes the command list into the wire shape
// poll_pdf_commands returns. Empty list emits "[]".
func marshalCommands(cmds []pdfCommand) string {
	b, _ := json.Marshal(cmds)
	return string(b)
}
