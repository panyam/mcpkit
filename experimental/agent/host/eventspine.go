package host

import (
	"sync"
	"sync/atomic"

	gocurrent "github.com/panyam/gocurrent"
)

// eventSpine is the per-session event backbone: the retained log every surface
// reads from, the synchronous local observers, and the offset marking how much
// of the log the RunStore already holds.
//
// The three belong together because replay is stitched from two sources that
// must agree. log covers [persisted, len) in memory; the RunStore covers
// [0, persisted). Reading them as a consistent pair is what rules out both the
// duplicate and the gap a two-step read would race — see Subscribe, which takes
// that snapshot under turnMu.
//
// The Queue itself stays reachable as log rather than being wrapped in
// delegating methods. Its barrier semantics (Resolve, AwaitResolution) carry
// the multi-surface ask protocol, and hiding them behind pass-throughs would
// obscure the contract callers actually reason about.
type eventSpine struct {
	log *gocurrent.Queue[HostEvent]

	// mu serializes local delivery only. The async server connections, the
	// turn goroutine, and the event goroutines all emit, and the terminal
	// renderer is not concurrency-safe.
	mu        sync.Mutex
	observers []Observer

	// persisted is the log offset through which this run's events have been
	// written to the RunStore. Advanced at the turn-end persist site once
	// AppendEvents succeeds; read lock-free by Subscribe. Stays 0 when no
	// RunStore is configured, so replay falls back to the whole window.
	persisted atomic.Int64
}

// emit records an event on the retained log and delivers it synchronously to
// every local observer, returning the offset it landed at.
//
// The append happens before the fan-out and outside the lock: the log is the
// source of truth a web surface subscribes onto, so it must record the event
// even if an observer blocks. The synchronous fan-out preserves the terminal
// rendering contract — a caller reading the renderer's io.Writer sees the event
// once emit returns.
func (s *eventSpine) emit(ev HostEvent) (offset int) {
	offset = s.log.Append(ev)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.observers {
		o.On(ev)
	}
	return offset
}
