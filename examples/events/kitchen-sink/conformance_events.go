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
	// build is the disposable one. Terminating a source is one-shot for the
	// life of the process, and the four events scenarios share a single
	// fixture, so a scenario that terminates chat.message or alert.fired
	// poisons whatever runs after it. build.finished exists only under this
	// flag, carries no feeder, and nothing else subscribes to it.
	build *events.YieldingSource[BuildFinishedData]
	// webhooks backs the tenant controls, which stand a subscription up on
	// another principal's behalf. The harness authenticates as exactly one
	// principal for a whole run, so without this the two-tenant case cannot be
	// constructed at all.
	webhooks *events.WebhookRegistry
}

// registerConformanceEventControls wires the diagnostic tools. Called only when
// --conformance-events is set.
// conformanceCallbackOrigins is where the events-webhook scenario points its
// callbacks (CALLBACK_BASE in the suite's webhook.ts). It never resolves, so
// it is only reachable through the allowlist.
var conformanceCallbackOrigins = []string{"https://conformance.invalid/mcp-events"}

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
		case "build.finished":
			return y.build, nil
		default:
			return nil, fmt.Errorf("no conformance control for event type %q (have chat.message, alert.fired, build.finished)", name)
		}
	}

	registerTenantControls(srv, y.webhooks)
	registerCallbackOriginControl(srv, y.webhooks)

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
		Description: "Conformance control: end every live subscription to an event type. Stream subscribers receive notifications/events/terminated and the stream closes. One-shot per source: the source stays terminated for the life of the process, so prefer build.finished, which nothing else uses.",
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

// registerTenantControls wires the two controls that need a second principal.
//
// sep-9999-subscribe-cross-tenant-isolation asserts that two tenants
// subscribing to the same event with the same callback get distinct
// subscriptions, because the principal is part of the key. A conformance run
// holds one principal for its lifetime, so the harness can supply one side of
// that comparison and not the other.
//
// Both controls use the same public identity functions the subscribe handler
// does, so what they register is addressable by the registry exactly as a real
// subscription would be. Nothing here bypasses the key rule; it supplies a
// principal the harness cannot authenticate as.
func registerTenantControls(srv *server.Server, webhooks *events.WebhookRegistry) {
	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_subscribe_as",
		Description: "Conformance control: register a webhook subscription on behalf of another principal, returning its derived subscription id. Lets a single-principal harness construct the two-tenant case.",
		InputSchema: tenantSchema(),
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		args, err := tenantArgsOf(req)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		key := events.CanonicalKey(args.Principal, args.URL, args.Name, nil)
		id := events.DeriveSubscriptionID(key)
		webhooks.Register(events.RegisterParams{
			CanonicalKey: key,
			DerivedID:    id,
			URL:          args.URL,
			Secret:       "whsec_Y29uZm9ybWFuY2UtdGVuYW50LWNvbnRyb2w",
			EventName:    args.Name,
			Principal:    args.Principal,
		})
		return core.TextResult(id), nil
	})

	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_subscription_exists",
		Description: "Conformance control: report whether a subscription for the given principal, event type and callback is still registered. Lets the harness prove one tenant's unsubscribe left another tenant's subscription alone.",
		InputSchema: tenantSchema(),
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		args, err := tenantArgsOf(req)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		want := events.DeriveSubscriptionID(
			events.CanonicalKey(args.Principal, args.URL, args.Name, nil),
		)
		for _, t := range webhooks.Targets() {
			if t.ID == want {
				return core.TextResult("true"), nil
			}
		}
		return core.TextResult("false"), nil
	})
}

// registerCallbackOriginControl lets the harness receive deliveries on its
// own loopback listener.
//
// events-webhook-delivery grades signing, headers, retries and verification,
// all of which are only observable from the endpoint the server POSTs to, so
// the harness has to be the receiver. It listens on loopback, which the
// fixture's SSRF guards correctly refuse, and that refusal is exactly what the
// suite's SSRF rows grade. The suite grades those rows first against the
// guards as configured, then calls this for its receiver's origin and
// subscribes again. Only that origin is lifted; every other callback is still
// refused, and the order means no SSRF verdict is taken after the override.
func registerCallbackOriginControl(srv *server.Server, webhooks *events.WebhookRegistry) {
	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_allow_callback_origin",
		Description: "Conformance control: permit webhook callbacks under one origin past the https and SSRF guards for the rest of the process, returning the origin permitted. Every other callback stays refused.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"origin": map[string]any{"type": "string", "description": "Origin to permit, e.g. http://127.0.0.1:53211. No path."},
			},
			"required":             []any{"origin"},
			"additionalProperties": false,
		},
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		var args struct {
			Origin string `json:"origin"`
		}
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return core.ErrorResult("arguments: " + err.Error()), nil
		}
		origin, err := webhooks.UnsafeAllowCallbackOrigin(args.Origin)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		return core.TextResult(origin), nil
	})
}

func tenantSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"principal": map[string]any{"type": "string", "description": "Principal to act as."},
			"name":      map[string]any{"type": "string", "description": "Event type."},
			"url":       map[string]any{"type": "string", "description": "Callback URL."},
		},
		"required":             []any{"principal", "name", "url"},
		"additionalProperties": false,
	}
}

type tenantArgs struct {
	Principal string `json:"principal"`
	Name      string `json:"name"`
	URL       string `json:"url"`
}

func tenantArgsOf(req core.ToolRequest) (tenantArgs, error) {
	var a tenantArgs
	if err := json.Unmarshal(req.Arguments, &a); err != nil {
		return a, fmt.Errorf("arguments: %w", err)
	}
	if a.Principal == "" || a.Name == "" || a.URL == "" {
		return a, fmt.Errorf("principal, name and url are all required")
	}
	return a, nil
}

func eventNameSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"enum":        []any{"chat.message", "alert.fired", "build.finished"},
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
