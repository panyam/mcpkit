package events

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
// This sits ALONGSIDE the spec's `notifications/events/list_changed`,
// which the registry also emits on every AddSource / RemoveSource (see
// broadcastListChanged). The two carry different information and are
// not substitutes: list_changed is a bare "re-fetch events/list" ping
// with no payload, while events.topology says which source appeared or
// disappeared and when, without a follow-up round trip.
//
// An earlier revision of this comment claimed list_changed "does not
// exist in the events SEP today" and used that to justify the
// meta-source as a replacement. That was wrong when written: the spec
// has carried a Dynamic Event Types section for a long time, and spec
// commit 28ec35e9 (2026-09-04) widened it to fire on descriptor
// changes too. The meta-source stays because it is strictly more
// informative, not because the spec surface was missing.
//
// The events.* reserved prefix signals "this name is SDK-owned, do not
// register a user source with this name."
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
	mu       sync.RWMutex
	srv      *server.Server
	webhooks *WebhookRegistry
	sources  map[string]EventSource
	// schemas holds each source's compiled EventDef.InputSchema, keyed
	// by source name. Compiled once at registration rather than per
	// request because compilation walks and resolves the whole schema
	// while validation only evaluates it. Absent entry means the source
	// declared no InputSchema, which the validator treats as accept-all.
	schemas map[string]*core.CompiledSchema
	// delivery holds each source's advertised delivery modes, keyed by
	// source name and derived at registration when the source declares
	// none. Kept here rather than written back onto the source because
	// EventSource is an interface: Def() may build a fresh EventDef on
	// every call, so there is nothing stable to mutate.
	delivery  map[string][]string
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
		schemas:  make(map[string]*core.CompiledSchema),
		delivery: make(map[string][]string),
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
		// The meta-source takes no subscription arguments, and says so
		// rather than leaving the field absent: a descriptor with no
		// inputSchema is indistinguishable from one whose schema nobody
		// wrote, and this is the one descriptor the library itself owns.
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	})
	r.wireLocked(metaSource)
	r.sources[TopologySourceName] = metaSource
	r.delivery[TopologySourceName] = deriveDelivery(metaSource, webhooks != nil)
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
	// Compile before taking the lock and before mutating the map, so a
	// malformed InputSchema fails registration outright instead of
	// leaving a source that rejects every request at dispatch time.
	compiled, err := core.CompileSchema(src.Def().InputSchema)
	if err != nil {
		return fmt.Errorf("events: AddSource %q: inputSchema: %w", name, err)
	}
	r.mu.Lock()
	if _, exists := r.sources[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("events: source %q already registered", name)
	}
	r.wireLocked(src)
	r.sources[name] = src
	r.delivery[name] = normalizeDelivery(src.Def().Delivery, src, r.webhooks != nil)
	if compiled != nil {
		r.schemas[name] = compiled
	}
	r.mu.Unlock()
	r.publishTopology(TopologyEventTypeAdded, name)
	r.broadcastListChanged()
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
	src, ok := r.sources[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("events: source %q not registered", name)
	}
	delete(r.sources, name)
	delete(r.schemas, name)
	delete(r.delivery, name)
	r.mu.Unlock()

	// Spec §"Event Type Removal and Breaking Changes" (commit 28ec35e9):
	// subscriptions to a removed type no longer hold a valid contract, so
	// the server SHOULD end them with each mode's termination signal
	// rather than leaving subscribers waiting on a name that is gone.
	//
	// Ordering matters. Terminate BEFORE announcing, so a client that
	// reacts to list_changed by re-reading events/list finds the removal
	// already reflected and its own subscription already closed, rather
	// than racing the two.
	r.terminateSubscriptions(name, src)

	r.publishTopology(TopologyEventTypeRemoved, name)
	r.broadcastListChanged()
	return nil
}

// terminateSubscriptions ends every live subscription to a removed event
// type, per spec §"Event Type Removal and Breaking Changes". Both modes
// carry the same error, -32011 NotFound with data.kind "event", which is
// what tells a client SDK to re-fetch events/list rather than treat the
// close as an auth failure or a transport blip.
//
// Poll needs no equivalent: it holds no server-side subscription, and a
// poll against a removed name already answers -32011 from the handler's
// own lookup miss.
func (r *Registry) terminateSubscriptions(name string, src EventSource) {
	// Webhook subscriptions live in the WebhookRegistry, keyed by event
	// name among other things.
	if r.webhooks != nil {
		r.webhooks.TerminateByEventName(name, ControlError{
			Code:    ErrCodeNotFound,
			Message: "event type " + name + " was removed",
			Data:    NotFoundData{Kind: "event"},
		})
	}
	// Push subscribers hold a channel handed out by the source itself, so
	// only the source can signal them. YieldingSource can; a source that
	// cannot implement the terminal signal simply closes its subscriber
	// channels when it shuts down, which streams already treat as an end.
	if t, ok := src.(sourceTerminator); ok {
		_ = t.YieldTerminated(EventDeliveryError{
			Code:    ErrCodeNotFound,
			Message: "event type " + name + " was removed",
			Data:    NotFoundData{Kind: "event"},
		})
	}
}

// sourceTerminator is implemented by sources that can push a terminal
// signal to their live stream subscribers. YieldingSource satisfies it
// for every payload type, since YieldTerminated's signature carries no
// type parameter.
type sourceTerminator interface {
	YieldTerminated(err EventDeliveryError) error
}

// broadcastListChanged emits the spec's `notifications/events/list_changed`
// (§"Dynamic Event Types") so clients know to re-read events/list. The
// notification carries no payload by design: it is a ping, and the client
// re-fetches to learn what actually changed.
//
// Fired on AddSource and RemoveSource. The spec also asks for it when an
// existing type's DESCRIPTOR changes in place, which mcpkit cannot reach
// today because there is no in-place mutation API on the registry.
func (r *Registry) broadcastListChanged() {
	if r.srv == nil {
		return
	}
	r.srv.Broadcast(context.Background(), "notifications/events/list_changed", map[string]any{})
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
// inputSchema returns the compiled InputSchema for a source, or nil
// when the source declared none. A nil result is the accept-all case:
// core.CompiledSchema.Validate is nil-safe and reports no violations,
// so callers can validate unconditionally without a presence check.
func (r *Registry) inputSchema(name string) *core.CompiledSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.schemas[name]
}

func (r *Registry) Source(name string) (EventSource, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sources[name]
	return s, ok
}

// Def returns the descriptor the server advertises for a source, which is the
// source's own EventDef with its delivery modes resolved.
//
// Read this rather than src.Def() wherever the answer has to match what
// events/list published. A source that declared no delivery modes gets a
// derived set at registration, and only the registry holds it.
func (r *Registry) Def(name string) (EventDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src, ok := r.sources[name]
	if !ok {
		return EventDef{}, false
	}
	def := src.Def()
	if d, ok := r.delivery[name]; ok {
		def.Delivery = d
	}
	return def, true
}

// normalizeDelivery resolves what a source advertises. A declared set is
// returned unchanged, including one that narrows to a single mode; the spec
// wants a non-empty subset of poll/push/webhook and an author who wrote one
// means it.
//
// An absent set is derived rather than defaulted to all three. Claiming push
// for a source that cannot stream would put the same kind of lie on the wire
// that an absent declaration does, one layer further in.
func normalizeDelivery(declared []string, src EventSource, hasWebhooks bool) []string {
	if len(declared) > 0 {
		return declared
	}
	return deriveDelivery(src, hasWebhooks)
}

// deriveDelivery reports the modes a source can actually serve. Poll is
// unconditional because Poll is on the EventSource interface. Push needs the
// optional Subscribe channel that registerStream requires. Webhook needs a
// registry to deliver through and the same fanout push uses.
func deriveDelivery(src EventSource, hasWebhooks bool) []string {
	modes := []string{DeliveryModePoll.String()}
	if _, ok := src.(streamSubscribable); ok {
		modes = append(modes, DeliveryModePush.String())
		if hasWebhooks {
			modes = append(modes, DeliveryModeWebhook.String())
		}
	}
	return modes
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
	names := make([]string, 0, len(r.sources))
	for name := range r.sources {
		names = append(names, name)
	}
	// Sorted, because this backs events/list and ranging the map put the
	// response in a different order on every call. #1408 was the same bug on
	// the apps bridge's tools/list. It stayed invisible here while the one
	// source a client could not poll happened to be excluded from selection;
	// giving events.topology a delivery array made the order load-bearing.
	sort.Strings(names)
	out := make([]EventSource, 0, len(names))
	for _, name := range names {
		out = append(out, r.sources[name])
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
