package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

// Diagnostic controls for the MCP Events conformance suite, registered only
// under --conformance-events.
//
// Several requirements describe what a server does when something goes wrong
// upstream: a transient failure, a retention gap, a subscription that ends.
// A harness cannot ask for any of those over the protocol, so without a way to
// provoke them the scenarios watch a window, see nothing, and report the rows
// untestable — which is red, by the policy in the suite's src/scenarios/
// untestable.ts, and stays red forever.
//
// The tools below provoke exactly those conditions. Each one calls public
// library API that any source author can reach, so the flag gates the trigger
// and never the behaviour: nothing here makes the server do something it could
// not otherwise do, it only gives the harness a way to ask.
//
// Off by default because this is a published example first. Someone reading it
// to learn the events API should not have to work out why a chat demo ships a
// tool for terminating subscriptions.
//
// The flag is per-surface rather than a bare --conformance so a fixture can
// serve several suites at once: --conformance-events --conformance-tasks
// composes, where a combined name would multiply with every surface added.
// See examples/CONVENTIONS.md § Conformance fixtures.

// conformanceYielders carries the handles the control tools drive. Sources are
// passed as the concrete yielding type rather than events.EventSource because
// the signals are on that type; an EventSource has no way to express them.
type conformanceYielders struct {
	chat  *events.YieldingSource[ChatMessageData]
	alert *events.YieldingSource[AlertData]
}

// registerConformanceEventControls wires the diagnostic tools. Called only when
// --conformance-events is set.
func registerConformanceEventControls(srv *server.Server, y conformanceYielders) {
	pick := func(name string) (interface {
		YieldError(events.EventDeliveryError) error
		YieldGap() error
		YieldTerminated(events.EventDeliveryError) error
	}, error) {
		switch name {
		case "chat.message":
			return y.chat, nil
		case "alert.fired":
			return y.alert, nil
		default:
			return nil, fmt.Errorf("no conformance control for event type %q (have chat.message, alert.fired)", name)
		}
	}

	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_yield_error",
		Description: "Conformance control: emit a transient upstream failure on an event type. Stream subscribers receive notifications/events/error and the subscription stays open.",
		InputSchema: eventNameSchema(),
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		name, err := eventNameOf(req)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		src, err := pick(name)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		if err := src.YieldError(events.EventDeliveryError{
			Code:    -32000,
			Message: "conformance: synthetic upstream failure",
		}); err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		return core.TextResult("ok: " + name), nil
	})

	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_yield_gap",
		Description: "Conformance control: signal that events were lost on an event type. Stream subscribers receive a fresh notifications/events/active with truncated:true and delivery continues.",
		InputSchema: eventNameSchema(),
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		name, err := eventNameOf(req)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		src, err := pick(name)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		if err := src.YieldGap(); err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		return core.TextResult("ok: " + name), nil
	})

	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_terminate",
		Description: "Conformance control: end every live subscription to an event type. Stream subscribers receive notifications/events/terminated and the stream closes. One-shot per source: the source stays terminated for the life of the process.",
		InputSchema: eventNameSchema(),
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		name, err := eventNameOf(req)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		src, err := pick(name)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		if err := src.YieldTerminated(events.EventDeliveryError{
			Code:    -32011,
			Message: "conformance: subscription terminated",
		}); err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		return core.TextResult("ok: " + name), nil
	})
}

func eventNameSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"enum":        []any{"chat.message", "alert.fired"},
				"description": "Event type to act on.",
			},
		},
		"required":             []any{"name"},
		"additionalProperties": false,
	}
}

// eventNameOf pulls the one argument these tools take. The declared schema
// already constrains it, so a failure here means the caller bypassed
// validation rather than mistyped.
func eventNameOf(req core.ToolRequest) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Arguments, &args); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	if args.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	return args.Name, nil
}
