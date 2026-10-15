package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

// CatalogData is the payload of the event types the catalog control mints.
type CatalogData struct {
	Value string `json:"value"`
}

// registerCatalogControl wires events_conformance_catalog, which changes the
// event catalog while the process runs.
//
// A change to the set of event types sends notifications/events/list_changed
// (SEP-3415 § Dynamic Event Types), and a healthy server never changes its
// catalog during a conformance run, so without this the suite has nothing to
// observe. "add" mints a fresh type per call rather than reusing one, and
// "remove" only accepts types this control minted, because removal ends every
// subscription to a type for the life of the process and the scenarios share
// one fixture (the same trap build.finished exists to avoid).
//
// Only add and remove for now. The in-place "extend" and "retype" ops that
// #1465 also asks for are not here yet.
func registerCatalogControl(srv *server.Server, registry *events.Registry) {
	var (
		mu     sync.Mutex
		next   int
		minted = map[string]bool{}
	)

	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_catalog",
		Description: "Conformance control: change the event catalog at runtime. op \"add\" registers a fresh event type and returns its name; op \"remove\" removes a type this control added. Both send notifications/events/list_changed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op":   map[string]any{"type": "string", "enum": []any{"add", "remove"}},
				"name": map[string]any{"type": "string", "description": "For remove: a name an earlier add returned."},
			},
			"required":             []any{"op"},
			"additionalProperties": false,
		},
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		var args struct {
			Op   string `json:"op"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return core.ErrorResult("arguments: " + err.Error()), nil
		}
		switch args.Op {
		case "add":
			mu.Lock()
			next++
			name := fmt.Sprintf("catalog.%d", next)
			mu.Unlock()
			src, _ := events.NewYieldingSource[CatalogData](events.EventDef{
				Name:        name,
				Description: "Conformance fixture: an event type added at runtime by events_conformance_catalog.",
				Delivery:    []string{"poll", "push"},
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"value": map[string]any{"type": "string", "description": "Deliver only occurrences with this value."},
					},
				},
			})
			if err := registry.AddSource(src); err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			mu.Lock()
			minted[name] = true
			mu.Unlock()
			return core.TextResult("ok: " + name), nil

		case "remove":
			mu.Lock()
			ok := minted[args.Name]
			delete(minted, args.Name)
			mu.Unlock()
			if !ok {
				return core.ErrorResult(fmt.Sprintf("%q was not added by events_conformance_catalog; only those can be removed", args.Name)), nil
			}
			if err := registry.RemoveSource(args.Name); err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			return core.TextResult("ok: " + args.Name), nil

		default:
			return core.ErrorResult(fmt.Sprintf("op must be \"add\" or \"remove\", got %q", args.Op)), nil
		}
	})
}
