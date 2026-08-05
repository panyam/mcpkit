package host

import (
	"context"

	"github.com/panyam/mcpkit/agent"
)

// Subscribe returns a channel that replays the session's history and then
// follows it live, so a surface attaching mid-session (a web streamer, issue
// 1196) sees the whole conversation and every subsequent event. The channel
// closes when ctx is cancelled; the caller must drain it or cancel ctx to
// release the backing goroutine and its Queue subscription.
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
// and going live is missed (the canonical subscribe-then-read order). Once the
// snapshot is captured the lock is released and all delivery happens off-lock in
// the goroutine, so a slow consumer never blocks a turn.
//
// When no RunStore is configured, persistedOffset stays 0, so the replay is
// simply the whole retained Queue window (ReadFrom(0)) followed by live — the
// pre-persistence behavior.
func (a *App) Subscribe(ctx context.Context) <-chan HostEvent {
	// Snapshot under turnMu so the persist site cannot advance persistedOffset
	// or the RunStore between the two reads below. Subscribe first so the
	// canonical subscribe-then-read order holds: any Append after this point
	// wakes the subscription (and is also picked up by the tail read, harmlessly
	// re-signalled), any Append before it is already in the tail.
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

	out := make(chan HostEvent)
	go func() {
		defer close(out)
		defer sub.Close()

		// (a) deep history from the RunStore.
		for _, e := range deep {
			select {
			case out <- HostEvent{Kind: HostRunnerEvent, RunnerEvent: e}:
			case <-ctx.Done():
				return
			}
		}
		// (b) the unpersisted Queue tail captured in the snapshot.
		for _, e := range tail {
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
				off = n
				for _, e := range evs {
					select {
					case out <- e:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out
}
