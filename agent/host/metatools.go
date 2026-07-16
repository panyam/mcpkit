package host

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/panyam/mcpkit/agent"
)

// registerMetaTools installs the async control-plane tools as a host-local
// FuncSource so the model can manage its own subscriptions, standing
// behaviors, and background tasks through ordinary tool calls. These are
// pure surface wiring over App state (subscription registry, TriggerPolicy,
// background-task registry) plus the agent-module runtime seams; nothing here
// is agent API. Registered under the source id "host".
func (a *App) registerMetaTools(multi *agent.MultiSource) error {
	fs := agent.NewFuncSource()

	type subReq struct {
		Server string `json:"server"`
		Name   string `json:"name"`
	}
	if err := agent.AddFunc(fs, "subscribe_events",
		"Subscribe to an event stream on a server so its occurrences enter your context. Returns a subscription id.",
		func(ctx context.Context, in subReq) (string, error) {
			id, err := a.openSubscription(context.Background(), in.Server, in.Name)
			if err != nil {
				return "", err
			}
			return "subscribed: " + id, nil
		}); err != nil {
		return err
	}

	type unsubReq struct {
		ID string `json:"id"`
	}
	if err := agent.AddFunc(fs, "unsubscribe",
		"Stop an event subscription by its id.",
		func(ctx context.Context, in unsubReq) (string, error) {
			if a.closeSubscription(in.ID) {
				return "unsubscribed: " + in.ID, nil
			}
			return "", fmt.Errorf("no subscription %q", in.ID)
		}); err != nil {
		return err
	}

	if err := agent.AddFunc(fs, "list_subscriptions",
		"List your active event subscriptions.",
		func(ctx context.Context, _ struct{}) (string, error) {
			subs := a.listSubscriptions()
			if len(subs) == 0 {
				return "no active subscriptions", nil
			}
			var b strings.Builder
			for _, s := range subs {
				fmt.Fprintf(&b, "%s (%s on %s)\n", s.id, s.name, s.server)
			}
			return b.String(), nil
		}); err != nil {
		return err
	}

	type triggerReq struct {
		Event        string            `json:"event"`
		Server       string            `json:"server,omitempty"`
		Filter       map[string]string `json:"filter,omitempty"`
		Instructions string            `json:"instructions"`
		Label        string            `json:"label"`
	}
	if err := agent.AddFunc(fs, "create_trigger",
		"Install a standing behavior: when a matching event fires, you are invoked with the given instructions. Use this for 'when X happens, do Y' (works for events and for task completions via the task.completed event).",
		func(ctx context.Context, in triggerReq) (string, error) {
			if in.Event == "" || in.Instructions == "" || in.Label == "" {
				return "", fmt.Errorf("event, instructions, and label are required")
			}
			b := agent.TriggerBinding{Server: in.Server, Event: in.Event, Instructions: in.Instructions, Label: in.Label}
			b.Filter = fieldEqualityFilter(in.Filter)
			a.triggers.Add(b)
			return "trigger installed: " + in.Label + " on " + in.Event, nil
		}); err != nil {
		return err
	}

	type removeTriggerReq struct {
		Server string `json:"server,omitempty"`
		Event  string `json:"event"`
		Label  string `json:"label"`
	}
	if err := agent.AddFunc(fs, "remove_trigger",
		"Remove a standing behavior by its event and label.",
		func(ctx context.Context, in removeTriggerReq) (string, error) {
			if a.triggers.Remove(in.Server, in.Event, in.Label) {
				return "trigger removed: " + in.Label, nil
			}
			return "", fmt.Errorf("no trigger %q on %q", in.Label, in.Event)
		}); err != nil {
		return err
	}

	if err := agent.AddFunc(fs, "list_triggers",
		"List your standing behaviors (triggers).",
		func(ctx context.Context, _ struct{}) (string, error) {
			bindings := a.triggers.Bindings()
			if len(bindings) == 0 {
				return "no triggers", nil
			}
			var b strings.Builder
			for _, tb := range bindings {
				fmt.Fprintf(&b, "%s: on %s → %s\n", tb.Label, tb.Event, tb.Instructions)
			}
			return b.String(), nil
		}); err != nil {
		return err
	}

	if err := agent.AddFunc(fs, "list_tasks",
		"List background tasks you have started that are still running.",
		func(ctx context.Context, _ struct{}) (string, error) {
			tasks := a.snapshotTasks()
			if len(tasks) == 0 {
				return "no background tasks", nil
			}
			var b strings.Builder
			for _, bt := range tasks {
				fmt.Fprintf(&b, "%s (%s) %s, running %s\n", bt.TaskID, bt.Tool, bt.Status(), time.Since(bt.StartedAt).Round(time.Second))
			}
			return b.String(), nil
		}); err != nil {
		return err
	}

	type cancelReq struct {
		ID string `json:"id"`
	}
	if err := agent.AddFunc(fs, "cancel_task",
		"Cancel a running background task by its id.",
		func(ctx context.Context, in cancelReq) (string, error) {
			a.tasksMu.Lock()
			bt := a.bgTasks[in.ID]
			a.tasksMu.Unlock()
			if bt == nil {
				return "", fmt.Errorf("no running task %q", in.ID)
			}
			if err := bt.Cancel(); err != nil {
				return "", err
			}
			return "cancelling: " + in.ID, nil
		}); err != nil {
		return err
	}

	return multi.Add("host", fs)
}

// fieldEqualityFilter builds an IncomingEvent predicate from top-level
// payload field equality checks (all must match). Nil/empty matches all.
func fieldEqualityFilter(want map[string]string) func(agent.IncomingEvent) bool {
	if len(want) == 0 {
		return nil
	}
	return func(ev agent.IncomingEvent) bool {
		for field, expect := range want {
			v, ok := ev.Data.Field(field)
			if !ok || strings.Trim(string(v.Raw()), `"`) != expect {
				return false
			}
		}
		return true
	}
}
