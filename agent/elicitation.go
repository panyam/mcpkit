package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// ElicitationUI renders one elicitation to the user and returns their
// answer. It is the single seam a surface implements: a terminal prompts
// inline, a web host forwards over its wire, a test scripts responses.
// Decline and cancel are results (ElicitationResult.Action), not errors;
// an error means the surface itself failed to present the request.
type ElicitationUI func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error)

// ElicitationCoordinator serializes elicitations from every connected server
// onto one ElicitationUI: exactly one request is presented at a time, waiters
// proceed in strict FIFO order, and a waiter whose ctx ends leaves the queue
// without disturbing it. Use one coordinator per user session so parallel
// tool calls (or multiple servers) never stack dialogs.
//
// Wiring covers both protocol inlets with one registration, because the
// client routes MRTR input_required rounds through the same dispatcher as
// real server-initiated requests:
//
//	coord := agent.NewElicitationCoordinator(ui)
//	c := client.NewClient(url, info,
//	    client.WithElicitationHandler(coord.Handler()))
//	src := agent.NewClientSource(c,
//	    agent.WithInputHandler(client.DefaultInputHandler(c)))
//
// With that, a legacy-wire server pushing elicitation/create and a
// stateless-wire (or task) server returning input_required both land on the
// same UI, serialized.
type ElicitationCoordinator struct {
	ui ElicitationUI

	mu      sync.Mutex
	busy    bool
	waiters []chan struct{}
}

// NewElicitationCoordinator wraps ui with FIFO serialization.
func NewElicitationCoordinator(ui ElicitationUI) *ElicitationCoordinator {
	return &ElicitationCoordinator{ui: ui}
}

// Handler adapts the coordinator to the client's elicitation option. Register
// it on every client whose servers may elicit.
func (c *ElicitationCoordinator) Handler() client.ElicitationHandler {
	return c.present
}

// Confirm presents a yes/no prompt through the same FIFO seam as elicitation
// and reports the user's choice. It is the general "ask the user to confirm"
// primitive; the approval ladder wires it as its AskFunc (WithAsk(coord.Confirm))
// so an approval prompt inherits the one-at-a-time serialization and never
// stacks against a concurrent elicitation. Only an explicit accept with
// confirm=true returns true; decline, cancel, or a missing/false field return
// false. A non-nil error means the surface failed to present the prompt.
func (c *ElicitationCoordinator) Confirm(ctx context.Context, message string) (bool, error) {
	res, err := c.present(ctx, core.ElicitationRequest{
		Message:         message,
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean"}},"required":["confirm"]}`),
	})
	if err != nil {
		return false, err
	}
	if res.Action != "accept" {
		return false, nil
	}
	confirmed, _ := res.Content["confirm"].(bool)
	return confirmed, nil
}

func (c *ElicitationCoordinator) present(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
	if err := c.acquire(ctx); err != nil {
		return core.ElicitationResult{}, err
	}
	defer c.release()
	return c.ui(ctx, req)
}

func (c *ElicitationCoordinator) acquire(ctx context.Context) error {
	c.mu.Lock()
	if !c.busy {
		c.busy = true
		c.mu.Unlock()
		return nil
	}
	turn := make(chan struct{})
	c.waiters = append(c.waiters, turn)
	c.mu.Unlock()

	select {
	case <-turn:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		for i, w := range c.waiters {
			if w == turn {
				c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
				c.mu.Unlock()
				return ctx.Err()
			}
		}
		// The baton was already handed to us; pass it on so the queue
		// does not stall.
		c.releaseLocked()
		c.mu.Unlock()
		return ctx.Err()
	}
}

func (c *ElicitationCoordinator) release() {
	c.mu.Lock()
	c.releaseLocked()
	c.mu.Unlock()
}

func (c *ElicitationCoordinator) releaseLocked() {
	if len(c.waiters) > 0 {
		next := c.waiters[0]
		c.waiters = c.waiters[1:]
		close(next)
		return
	}
	c.busy = false
}
