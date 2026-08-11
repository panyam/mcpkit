package host

import (
	"context"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

// elicitResolution is a pending ask's answer, delivered by whichever surface
// responds first (the barrier resolution value on the event log). err is a
// surface-presentation failure (not a decline, which is an
// ElicitationResult.Action); by names the resolving surface.
type elicitResolution struct {
	res core.ElicitationResult
	err error
	by  string
}

// RespondToAsk resolves a pending elicitation, identified by the event-log
// offset the ask was announced at (a surface reads that offset from its Watch
// frame). Resolution rides the log's barrier, so the first responder wins and a
// stale or already-answered offset returns the log's ErrAlreadyResolved /
// ErrOffsetOutOfRange. This is the data-only capability a web surface's respond
// RPC calls (issue 1196); the local terminal UI resolves the same ask through
// barrierElicit below.
func (a *App) RespondToAsk(askOffset int, res core.ElicitationResult, by string) error {
	return a.events.log.Resolve(askOffset, elicitResolution{res: res, by: by})
}

// barrierElicit wraps a local ElicitationUI so an elicitation is broadcast to
// every surface (HostElicitRequest, at a known log offset) and resolved by the
// first responder. The ask is a log entry; its resolution is the log's barrier
// on that entry's offset. The local UI runs as one responder, racing any
// surface that calls RespondToAsk on that offset; the log's Resolve makes the
// first win and cancels the loser. A HostElicitResolved event then tells every
// surface to retract. With no other surface attached the local UI always wins,
// so single-surface behavior is unchanged.
func (a *App) barrierElicit(local agent.ElicitationUI) agent.ElicitationUI {
	return func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		reqCopy := req
		off := a.emit(HostEvent{Kind: HostElicitRequest, Elicit: &reqCopy})

		// The local UI is one responder; cancel it when another surface wins.
		localCtx, cancelLocal := context.WithCancel(ctx)
		defer cancelLocal()
		go func() {
			res, err := local(localCtx, req)
			if localCtx.Err() == nil {
				a.events.log.Resolve(off, elicitResolution{res: res, err: err, by: "local"})
			}
		}()

		// AwaitResolution has no ctx, so run it on a goroutine and abort a
		// cancelled turn by resolving the ask ourselves (the goroutine then
		// unblocks and exits; the buffered channel means it never leaks).
		resolved := make(chan elicitResolution, 1)
		go func() { resolved <- a.events.log.AwaitResolution(off).(elicitResolution) }()

		select {
		case r := <-resolved:
			cancelLocal()
			if r.by == askCancelled {
				return core.ElicitationResult{}, r.err
			}
			a.emit(HostEvent{Kind: HostElicitResolved, AskID: int64(off), By: r.by})
			return r.res, r.err
		case <-ctx.Done():
			a.events.log.Resolve(off, elicitResolution{err: ctx.Err(), by: askCancelled})
			return core.ElicitationResult{}, ctx.Err()
		}
	}
}

// askCancelled is the resolver tag for a turn cancelled before any surface
// answered; it unblocks the AwaitResolution goroutine without emitting a
// HostElicitResolved (nothing was answered).
const askCancelled = "cancelled"
