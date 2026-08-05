package host

import (
	"context"

	"github.com/panyam/mcpkit/agent"
)

// subscribeBuffer smooths bursts on a Subscribe channel. It is not a
// correctness bound: the drain goroutine reads the retained event log at its
// own pace, so even a full buffer only slows the drain, never emit.
const subscribeBuffer = 64

// Subscribe returns a channel that replays the session's history and then
// follows it live, so a surface attaching mid-session (a web streamer, issue
// 1196) sees the whole conversation and every subsequent event. The channel
// closes when ctx is cancelled; the caller must drain it or cancel ctx to
// release the backing goroutine and its Queue subscription. A dropped consumer
// that wants the stream again simply calls Subscribe once more.
//
// Local observers stay synchronous (issue 1194, emit's in-line fan-out); this
// is the async seam kept separate so a slow remote consumer never stalls the
// terminal rendering contract. A slow consumer cannot block emit: emit only
// Appends (non-blocking) and fans out to local observers, while this drain
// goroutine reads the log independently and blocks only itself.
//
// Replay is stitched from two sources so a bounded (evicting) event log stays
// lossless for a persisted session:
//
//   - Deep history from the RunStore. When a RunStore is configured and the
//     run has persisted events, each stored agent.Event is replayed wrapped as
//     HostEvent{Kind: HostRunnerEvent} — the turns already written to the store,
//     which may have since evicted from the in-memory Queue. Non-runner
//     HostEvents (server-state, skills) from those turns are ephemeral and
//     intentionally not replayed; the RunStore only holds the runner stream.
//   - The unpersisted tail from the Queue, drained from persistedOffset — the
//     in-progress turn plus any recent events not yet written to the store.
//     With a generous retention window this range is always still retained.
//
// The two halves meet exactly at persistedOffset with no overlap: the RunStore
// covers eventLog[0, persistedOffset), the Queue drain starts at
// persistedOffset. After the tail, the goroutine follows the Queue's Notify for
// live events.
//
// Consistency (snapshot-then-stream): the snapshot — subscribing to the Queue,
// reading persistedOffset, and reading the RunStore — is taken under turnMu,
// which the turn-end persist site also holds while it advances persistedOffset
// and writes AppendEvents together. So a persist cannot interleave the snapshot:
// the RunStore contents and persistedOffset are always read as one consistent
// pair, ruling out the dup (RunStore ahead of persistedOffset) and gap
// (persistedOffset ahead of RunStore) a two-step read would race. Subscribing to
// the Queue before releasing turnMu means no live Append between the tail read
// and going live is missed. Once the snapshot is captured the lock is released
// and all delivery happens off-lock, so a slow consumer never blocks a turn.
//
// When no RunStore is configured, persistedOffset stays 0, so the replay is
// simply the whole retained Queue window (ReadFrom(0)) followed by live.
//
// Ask ids: HostElicitRequest is appended without an id, so its event-log offset
// is stamped onto the delivered copy here (stampAskID) — the offset is the id
// E2's RespondToAsk resolves through the log barrier. Deep-history events are
// runner events, never asks, so only the Queue tail and live events are stamped.
func (a *App) Subscribe(ctx context.Context) <-chan HostEvent {
	// Snapshot under turnMu so the persist site cannot advance persistedOffset
	// or the RunStore between the reads below. Subscribe first so any Append
	// after this point wakes the subscription (and is also picked up by the tail
	// read, harmlessly re-signalled); any Append before it is already in the tail.
	a.turnMu.Lock()
	sub := a.eventLog.Subscribe()
	p := int(a.persistedOffset.Load())
	var deep []agent.Event
	if a.store != nil && a.runID != "" && p > 0 {
		if resp, err := a.store.LoadRun(ctx, agent.LoadRunRequest{RunID: a.runID}); err == nil && resp.Found {
			deep = resp.Run.Events
		}
	}
	tail, next := a.eventLog.ReadFrom(p)
	a.turnMu.Unlock()

	out := make(chan HostEvent, subscribeBuffer)
	go func() {
		defer close(out)
		defer sub.Close()

		// (a) deep history from the RunStore: runner events, never asks.
		for _, e := range deep {
			select {
			case out <- HostEvent{Kind: HostRunnerEvent, RunnerEvent: e}:
			case <-ctx.Done():
				return
			}
		}
		// (b) the unpersisted Queue tail captured in the snapshot. tail[i] is at
		// log offset p+i.
		for i, e := range tail {
			stampAskID(&e, p+i)
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
		// (c) live: follow the Queue from where the tail ended.
		off := next
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Notify():
				evs, n := a.eventLog.ReadFrom(off)
				for i, e := range evs {
					stampAskID(&e, off+i)
					select {
					case out <- e:
					case <-ctx.Done():
						return
					}
				}
				off = n
			}
		}
	}()
	return out
}

// stampAskID stamps the event-log offset as the ask id on a HostElicitRequest,
// so a remote surface reads the id off the frame and answers via
// RespondToAsk(offset). A no-op for other kinds. ev points at a delivered copy
// (HostEvent is a value), never the stored log entry.
func stampAskID(ev *HostEvent, offset int) {
	if ev.Kind == HostElicitRequest {
		ev.AskID = int64(offset)
	}
}
