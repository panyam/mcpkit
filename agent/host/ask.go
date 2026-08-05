package host

import (
	"context"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// elicitResolution is a pending ask's answer, delivered by whichever surface
// responds first. err is the surface-presentation error (not a decline, which
// is an ElicitationResult.Action); by names the resolving surface.
type elicitResolution struct {
	res core.ElicitationResult
	err error
	by  string
}

// pendingAsk is one outstanding elicitation awaiting a response. once makes the
// first responder win: later Resolve calls are dropped.
type pendingAsk struct {
	ch   chan elicitResolution
	once sync.Once
}

// registerAsk allocates an ask id and its one-shot resolution channel.
func (a *App) registerAsk() (int64, *pendingAsk) {
	a.asksMu.Lock()
	defer a.asksMu.Unlock()
	a.askSeq++
	id := a.askSeq
	p := &pendingAsk{ch: make(chan elicitResolution, 1)}
	a.asks[id] = p
	return id, p
}

func (a *App) clearAsk(id int64) {
	a.asksMu.Lock()
	delete(a.asks, id)
	a.asksMu.Unlock()
}

func (a *App) lookupAsk(id int64) *pendingAsk {
	a.asksMu.Lock()
	defer a.asksMu.Unlock()
	return a.asks[id]
}

// RespondToAsk resolves a pending elicitation (identified by the AskID a
// surface read off the HostElicitRequest event) with a surface-supplied answer.
// The first responder wins; a response to an unknown or already-answered ask
// returns an error (app state, so a surface can report "already answered" or
// "no such ask" rather than fail). This is the data-only capability a web
// surface's respond RPC calls (issue 1196); the local terminal UI resolves the
// same ask through the barrier below.
func (a *App) RespondToAsk(id int64, res core.ElicitationResult, by string) error {
	p := a.lookupAsk(id)
	if p == nil {
		return fmt.Errorf("host: no pending ask %d", id)
	}
	if !p.resolve(elicitResolution{res: res, by: by}) {
		return fmt.Errorf("host: ask %d already answered", id)
	}
	return nil
}

// resolve delivers r to the ask exactly once; it reports whether this caller
// was the winner.
func (p *pendingAsk) resolve(r elicitResolution) bool {
	won := false
	p.once.Do(func() {
		p.ch <- r
		won = true
	})
	return won
}

// barrierElicit wraps a local ElicitationUI so an elicitation is broadcast to
// every surface (HostElicitRequest) and resolved by the first responder. The
// local UI runs as one responder, racing any surface that calls RespondToAsk;
// whichever answers first wins and the other is cancelled. A HostElicitResolved
// event then tells every surface to retract its prompt. With no other surface
// attached the local UI always wins, so single-surface behavior is unchanged.
func (a *App) barrierElicit(local agent.ElicitationUI) agent.ElicitationUI {
	return func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		id, p := a.registerAsk()
		defer a.clearAsk(id)

		reqCopy := req
		a.emit(HostEvent{Kind: HostElicitRequest, AskID: id, Elicit: &reqCopy})

		// The local UI is one responder; cancel it when a remote surface wins.
		localCtx, cancelLocal := context.WithCancel(ctx)
		defer cancelLocal()
		go func() {
			res, err := local(localCtx, req)
			if localCtx.Err() != nil {
				return // cancelled because a remote surface already won
			}
			p.resolve(elicitResolution{res: res, err: err, by: "local"})
		}()

		select {
		case r := <-p.ch:
			cancelLocal()
			a.emit(HostEvent{Kind: HostElicitResolved, AskID: id, By: r.by})
			return r.res, r.err
		case <-ctx.Done():
			return core.ElicitationResult{}, ctx.Err()
		}
	}
}
