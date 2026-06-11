package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// TopologySourceName is the SDK-reserved name of the self-registered
// meta-source whose events report the lifecycle of every OTHER source
// on the server. Subscribers to events.topology see one TopologyEvent
// per successful AddSource / RemoveSource call.
//
// This is mcpkit's stand-in for the spec-shaped
// `notifications/events/list_changed` notification, which does not
// exist in the events SEP today. By making source lifecycle a normal
// event stream the SDK avoids inventing protocol surface: any client
// that can subscribe to a source can observe topology, and the events.*
// reserved prefix signals "this name is SDK-owned, do not register a
// user source with this name."
//
// The events.* prefix is reserved for SDK-internal sources. Callers
// must not register an EventSource with a name starting with `events.`
// — AddSource rejects with an error.
const TopologySourceName = "events.topology"

const reservedSourceNamePrefix = "events."

// TopologyEvent is the payload yielded on events.topology when a
// source is registered or unregistered at runtime. Cursored so a
// late-joining subscriber can replay recent topology changes; the
// payload carries enough provenance to drive an admin-facing topology
// view without polling events/list.
type TopologyEvent struct {
	// Type is "source.added" or "source.removed". Stable string
	// constants so subscribers can switch on them without a wire-
	// shape change as future event types are added (e.g.,
	// "source.suspended" if the SDK ever surfaces that state).
	Type string `json:"type"`
	// Name is the Def().Name of the source that was added or
	// removed.
	Name string `json:"name"`
	// Timestamp is the RFC3339Nano timestamp of the mutation, taken
	// at the moment AddSource / RemoveSource updated the registry
	// (NOT when the subscriber received the event).
	Timestamp string `json:"ts"`
}

// TopologyEventTypeAdded / Removed are the canonical Type values
// yielded on events.topology. Exposed as constants so subscribers can
// switch on the value without copying string literals.
const (
	TopologyEventTypeAdded   = "source.added"
	TopologyEventTypeRemoved = "source.removed"
)

// Registry is the runtime handle Register returns. It owns the
// thread-safe source map the per-mode dispatchers consult on every
// request, and exposes AddSource / RemoveSource so authors can
// reconfigure the source topology after Register has run.
//
// Backward compatibility: existing callers of Register that ignore the
// return value see no behavior change — Register still wires every
// source from cfg.Sources at registration time. AddSource is purely
// additive on top of that initial set.
//
// Typical runtime-add use case: an admin endpoint receives a request to
// connect to a real upstream (Discord, Telegram, custom HTTP polling),
// constructs the corresponding EventSource + lifecycle resources (e.g.,
// discordgo.Session), opens the upstream connection, then calls
// AddSource. RemoveSource reverses the registry side; the caller closes
// the upstream connection separately — the events package does NOT own
// the source's external resources.
//
// Concurrency: AddSource / RemoveSource take an internal write lock;
// per-request Source lookups inside the dispatchers take a read lock.
// A source added during a concurrent dispatch is either fully visible
// or not at all — there is no half-registered state on the wire.
//
// Topology observability: every Registry self-registers a meta-source
// at TopologySourceName ("events.topology"). Each successful AddSource
// / RemoveSource yields a TopologyEvent on it, so any client subscribed
// to events.topology sees the live source-lifecycle stream without a
// dedicated protocol surface. See TopologyEvent for the payload shape.
type Registry struct {
	mu        sync.RWMutex
	srv       *server.Server
	webhooks  *WebhookRegistry
	sources   map[string]EventSource
	emitter   Emitter
	tp        core.TracerProvider
	metaYield func(context.Context, TopologyEvent) error
}

// newRegistry constructs a Registry from the resolved Config values.
// Called once from Register; not exported because callers should not
// construct a Registry directly — Register also wires the per-mode
// dispatchers against the returned Registry, and a hand-built one
// would have no dispatchers attached.
func newRegistry(srv *server.Server, webhooks *WebhookRegistry, emitter Emitter, tp core.TracerProvider) *Registry {
	r := &Registry{
		srv:      srv,
		webhooks: webhooks,
		sources:  make(map[string]EventSource),
		emitter:  emitter,
		tp:       tp,
	}
	// Self-register the topology meta-source. Bypasses AddSource so the
	// meta-source itself doesn't yield a "source.added" event for its
	// own creation (philosophical knot we'd rather not invite). The
	// reserved-name check inside AddSource also rejects events.*
	// callers, so this is the only place the topology source is
	// constructed.
	metaSource, metaYield := NewYieldingSource[TopologyEvent](EventDef{
		Name:        TopologySourceName,
		Description: "SDK meta-source. Yields a TopologyEvent for every AddSource / RemoveSource against this server's Registry. Use to observe source lifecycle without polling events/list.",
	})
	r.wireLocked(metaSource)
	r.sources[TopologySourceName] = metaSource
	r.metaYield = metaYield
	return r
}

// AddSource registers a new EventSource at runtime. Returns an error
// if a source with the same Def().Name is already registered, or if
// src is nil / its Def().Name is empty.
//
// On success the source is immediately discoverable via the
// events/list dispatcher and routable by the events/poll, events/stream,
// and events/subscribe dispatchers. The Emitter and TracerProvider
// configured at Register time are wired into the new source — same
// per-source setup the initial Register loop does.
//
// Caller owns the source's external lifecycle. For sources backed by
// network connections (Discord gateway, Telegram long-poll, etc.) the
// expected pattern is: open the connection, construct the EventSource,
// AddSource, ... later ... RemoveSource, close the connection.
func (r *Registry) AddSource(src EventSource) error {
	if src == nil {
		return errors.New("events: AddSource: nil source")
	}
	name := src.Def().Name
	if name == "" {
		return errors.New("events: AddSource: source.Def().Name is empty")
	}
	if strings.HasPrefix(name, reservedSourceNamePrefix) {
		return fmt.Errorf("events: AddSource: name %q uses the reserved %q prefix", name, reservedSourceNamePrefix)
	}
	r.mu.Lock()
	if _, exists := r.sources[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("events: source %q already registered", name)
	}
	r.wireLocked(src)
	r.sources[name] = src
	r.mu.Unlock()
	r.publishTopology(TopologyEventTypeAdded, name)
	return nil
}

// RemoveSource unregisters a source by name. Returns an error if no
// source with that name is currently registered. After RemoveSource
// returns, the source is no longer discoverable via events/list and
// will not be matched by events/poll, events/stream, or
// events/subscribe — subsequent requests targeting the removed source
// receive the usual "unknown source" error.
//
// In-flight requests already inside a dispatcher at the moment of
// removal complete against the source they read at lookup time; the
// caller should treat removal as eventually-consistent for delivery.
//
// Caller owns external lifecycle teardown (close Discord session,
// stop long-polling goroutine, etc.) — the events package does NOT
// reach into the EventSource to terminate anything beyond removing
// its registry entry.
func (r *Registry) RemoveSource(name string) error {
	if strings.HasPrefix(name, reservedSourceNamePrefix) {
		return fmt.Errorf("events: RemoveSource: %q uses the reserved %q prefix and cannot be removed", name, reservedSourceNamePrefix)
	}
	r.mu.Lock()
	if _, ok := r.sources[name]; !ok {
		r.mu.Unlock()
		return fmt.Errorf("events: source %q not registered", name)
	}
	delete(r.sources, name)
	r.mu.Unlock()
	r.publishTopology(TopologyEventTypeRemoved, name)
	return nil
}

// publishTopology yields a TopologyEvent on the events.topology meta-
// source. Called from AddSource / RemoveSource AFTER the registry
// mutation lands (outside the registry lock) so a slow subscriber
// can't stall further admin operations. A failed yield is logged at
// the YieldingSource level and ignored here — topology delivery is
// best-effort observability, not a correctness gate on the mutation.
func (r *Registry) publishTopology(eventType, name string) {
	if r.metaYield == nil {
		return // belt-and-suspenders; newRegistry always sets it
	}
	_ = r.metaYield(context.Background(), TopologyEvent{
		Type:      eventType,
		Name:      name,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// Source looks up a registered source by name. Returns (nil, false)
// when no source with that name is currently registered.
func (r *Registry) Source(name string) (EventSource, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sources[name]
	return s, ok
}

// SourceNames returns the names of all currently registered sources in
// no particular order. Useful for diagnostic / admin handlers that
// want to list what's installed.
func (r *Registry) SourceNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.sources))
	for name := range r.sources {
		out = append(out, name)
	}
	return out
}

// snapshot returns the registered sources as a slice. Internal — used
// by registerList's dispatcher to build the events/list response from
// the current registry state on every call (so a runtime-added source
// shows up on the next list without re-registering the dispatcher).
func (r *Registry) snapshot() []EventSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]EventSource, 0, len(r.sources))
	for _, s := range r.sources {
		out = append(out, s)
	}
	return out
}

// wireLocked attaches per-source plumbing — emit hook + TracerProvider
// — for a source being added to the registry. Mirrors the per-source
// loop body inside Register. Caller MUST hold r.mu for write.
func (r *Registry) wireLocked(src EventSource) {
	if ea, ok := src.(emitterAware); ok {
		ea.SetEmitHook(func(ctx context.Context, event Event) {
			_ = r.emitter.Emit(ctx, event)
		})
	}
	if r.tp != nil {
		if installer, ok := src.(TracerProviderInstaller); ok {
			installer.SetTracerProvider(r.tp)
		}
	}
}
