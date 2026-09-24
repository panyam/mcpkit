package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	gohttp "github.com/panyam/servicekit/http"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

// Restart support for the MCP Events TTL rows, registered only under
// --conformance-events.
//
// Three requirements are about what survives a restart: a long grant is kept
// for its lifetime, a no-expiry subscription is persisted, and one that keeps
// failing is eventually dropped. A harness cannot restart a server over the
// wire, so this gives it a control that does the next best thing through
// public API only: throw away the whole server and webhook registry and build
// new ones over the same WebhookStore. That is what a real deployment with an
// external store (Postgres through stores/gorm) looks like across a process
// restart, which is the case the requirements are written for. Whether a store
// actually reaches disk is the store's business and is graded by its own tests.

// conformanceGCWindow is how long a no-expiry subscription may fail before the
// registry drops it. The library default is 72 hours, which no conformance run
// can wait out; 2s lets the suite watch the drop inside one scenario.
const conformanceGCWindow = 2 * time.Second

// conformanceRuntime is what outlives a restart: the store, and the hook the
// restart control calls. restart is nil when the process cannot restart
// itself (the e2e tests build servers directly).
type conformanceRuntime struct {
	store   events.WebhookStore
	restart func()
	// generation counts builds, starting at 1. The restart control answers
	// the generation it is moving to and the generation control the current
	// one, so a harness can tell the restart landed without depending on
	// whether its connection has a session to lose.
	generation atomic.Int64
}

func newConformanceRuntime() *conformanceRuntime {
	rt := &conformanceRuntime{store: events.NewInMemoryWebhookStore()}
	rt.generation.Store(1)
	return rt
}

// webhookOptions are the registry settings the TTL rows need. No-expiry grants
// are opt-in in the library; the GC window is shortened as described above.
func (rt *conformanceRuntime) webhookOptions() []events.WebhookOption {
	return []events.WebhookOption{
		events.WithWebhookStore(rt.store),
		events.WithAllowInfiniteWebhookTTL(),
		events.WithNoExpiryFailureGCWindow(conformanceGCWindow),
	}
}

func registerRestartControls(srv *server.Server, rt *conformanceRuntime) {
	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_restart",
		Description: "Conformance control: rebuild the server and its webhook registry over the same subscription store, as a process restart would. Every session ends; reconnect before the next request.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		if rt.restart == nil {
			return core.ErrorResult("this process cannot restart itself"), nil
		}
		next := rt.generation.Load() + 1
		rt.restart()
		return core.TextResult(strconv.FormatInt(next, 10)), nil
	})

	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_generation",
		Description: "Conformance control: the current build generation, which the restart control's answer is compared against.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult(strconv.FormatInt(rt.generation.Load(), 10)), nil
	})

	srv.RegisterTool(core.ToolDef{
		Name:        "events_conformance_subscription_state",
		Description: "Conformance control: report whether the subscription with this derived id is active, suspended after delivery failures, or absent from the store.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Derived subscription id from events/subscribe."},
			},
			"required":             []any{"id"},
			"additionalProperties": false,
		},
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Arguments, &args); err != nil || args.ID == "" {
			return core.ErrorResult("id is required"), nil
		}
		list, err := rt.store.ListWebhooks(ctx, events.ListWebhooksRequest{})
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		for _, t := range list.Targets {
			if t.ID != args.ID {
				continue
			}
			if t.Status.Active {
				return core.TextResult("active"), nil
			}
			return core.TextResult("suspended"), nil
		}
		return core.TextResult("absent"), nil
	})
}

// conformanceServer serves whichever build is current, and swaps in a fresh
// one on restart. Sessions die with the build that owned them, as they would
// with the process.
type conformanceServer struct {
	addr    string
	tp      core.TracerProvider
	rt      *conformanceRuntime
	feeders func(ctx context.Context, w *wiredServer)

	current atomic.Pointer[http.Handler]
	mu      sync.Mutex
	stop    context.CancelFunc
	srv     *server.Server
}

func newConformanceServer(addr string, tp core.TracerProvider, feeders func(context.Context, *wiredServer)) *conformanceServer {
	c := &conformanceServer{addr: addr, tp: tp, rt: newConformanceRuntime(), feeders: feeders}
	// Deferred so the tool call that asked for the restart gets its answer
	// from the build it was made on.
	c.rt.restart = func() { time.AfterFunc(100*time.Millisecond, c.rebuild) }
	c.rebuild()
	return c
}

// rebuild constructs a new build over the shared store and makes it current.
// The --wire request logging that the demo path wraps around the handler is
// not applied here.
func (c *conformanceServer) rebuild() {
	w := buildServerWith(c.addr, c.tp, true, c.rt)
	mux := http.NewServeMux()
	mux.Handle("/mcp", w.srv.Handler(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
		server.WithEventStore(gohttp.NewMemoryEventStore(eventStoreCap)),
	))
	mux.HandleFunc("POST /inject", injectHandlerFor(w))
	var h http.Handler = mux

	ctx, cancel := context.WithCancel(context.Background())
	if c.feeders != nil {
		c.feeders(ctx, w)
	}

	c.mu.Lock()
	oldStop, oldSrv := c.stop, c.srv
	c.stop, c.srv = cancel, w.srv
	c.current.Store(&h)
	c.mu.Unlock()

	if oldStop != nil {
		c.rt.generation.Add(1)
		oldStop()
		oldSrv.CloseAllSessions()
		log.Printf("[conformance] restarted: new server and registry over the same subscription store")
	}
}

func (c *conformanceServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*c.current.Load()).ServeHTTP(w, r)
}
