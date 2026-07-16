package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// startEventStreams opens one events/stream per configured event and feeds
// occurrences into the app's event channel. Stream callbacks must not block
// (they run on the per-call SSE reader), so they only convert and enqueue;
// the app's consumer goroutine runs the policies. A full channel drops the
// event with a transcript warning: backpressure must never stall the SSE
// reader, and the injection policy's windows make individual losses benign.
func (a *App) startEventStreams(ctx context.Context) error {
	for i, sc := range a.cfg.Servers {
		for _, ec := range sc.Events {
			serverID := sc.ID
			opts := eventsclient.StreamOptions{
				EventName: ec.Name,
				OnEvent: func(ev eventsclientEvent) {
					in := adaptEvent(serverID, ev)
					select {
					case a.events <- in:
					default:
						a.renderer.eventDropped(serverID, in.Name)
					}
				},
			}
			call, err := eventsclient.Stream(ctx, a.clients[i], opts)
			if err != nil {
				return fmt.Errorf("agentchat: events/stream %s on %s: %w", ec.Name, serverID, err)
			}
			a.streams = append(a.streams, call)
		}
	}
	return nil
}

// eventsclientEvent aliases the wire event type so adaptEvent's signature
// stays readable.
type eventsclientEvent = events.Event

// consumeEvents is the single goroutine that owns the policy pipeline:
// ingest into injection, evaluate triggers, and run approved proactive
// turns (serialized against user turns by the turn mutex inside
// runProactiveTurn).
func (a *App) consumeEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-a.events:
			if !ok {
				return
			}
			a.injection.Ingest(ev)
			if firing := a.triggers.OnEvent(ev); firing != nil {
				a.runProactiveTurn(ctx, firing)
			}
		}
	}
}

// runProactiveTurn executes a trigger firing as a full turn: the binding's
// instructions enter history as a system message (alongside any drained
// event context), the model responds, and the transcript marks the turn
// with the binding's label.
func (a *App) runProactiveTurn(ctx context.Context, firing *agent.TriggerFiring) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.renderer.triggerFired(firing.Binding.Label)
	a.history = append(a.history, agent.Message{Role: agent.RoleSystem, Text: firing.Binding.Instructions})
	a.drainInjectionLocked()
	result, err := a.runner.Run(ctx, a.history, a.renderer.handle)
	if err != nil {
		a.renderer.turnFailed(err)
		return
	}
	a.history = append(a.history, result.Messages...)
	a.renderer.turnDone(result)
	a.renderer.prompt()
}

// drainInjectionLocked moves pending injected context into history as
// system messages. Caller holds turnMu.
func (a *App) drainInjectionLocked() {
	for _, inj := range a.injection.Drain() {
		a.history = append(a.history, agent.Message{Role: agent.RoleSystem, Text: inj.Text})
	}
}

// buildTriggerBindings converts config bindings into policy bindings,
// rendering the equality filter against top-level payload fields.
func buildTriggerBindings(cfgs []TriggerConfig) []agent.TriggerBinding {
	out := make([]agent.TriggerBinding, 0, len(cfgs))
	for _, tc := range cfgs {
		b := agent.TriggerBinding{
			Server:       tc.Server,
			Event:        tc.Event,
			Instructions: tc.Instructions,
			Label:        tc.Label,
			Cooldown:     time.Duration(tc.CooldownSec) * time.Second,
		}
		if len(tc.Filter) > 0 {
			want := tc.Filter
			b.Filter = func(ev agent.IncomingEvent) bool {
				for field, expect := range want {
					v, ok := ev.Data.Field(field)
					if !ok || strings.Trim(string(v.Raw()), `"`) != expect {
						return false
					}
				}
				return true
			}
		}
		out = append(out, b)
	}
	return out
}

// hintOverrides collects the per-event hint overrides from config.
func hintOverrides(cfg *Config) map[string]agent.ContextHint {
	out := map[string]agent.ContextHint{}
	for _, sc := range cfg.Servers {
		for _, ec := range sc.Events {
			if ec.Hint != nil {
				out[ec.Name] = *ec.Hint
			}
		}
	}
	return out
}

func adaptEvent(serverID string, ev eventsclientEvent) agent.IncomingEvent {
	return agent.IncomingEvent{
		Server: serverID,
		Name:   ev.Name,
		ID:     ev.EventID,
		Cursor: ev.CursorStr(),
		Time:   time.Now(),
		Data:   core.NewRawJSON(json.RawMessage(ev.Data)),
		Meta:   ev.Meta,
	}
}
