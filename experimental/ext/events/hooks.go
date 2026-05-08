package events

// Hook surface for per-event-type author behavior — match / transform on
// broadcast emit and on_subscribe / on_unsubscribe on lifecycle (η-2).
//
// Design context:
//
//   - Spec §"Server SDK Guidance" L565+ describes hooks in Python prose.
//     Idioms 3 and 5 (class-bundled match/transform; separate
//     @on_subscribe decorator) collapse onto a single Go answer: fields
//     on EventDef. See docs/EVENTS_ETA_PLAN.md Q1.
//
//   - Hooks are sync per Q3 — goroutines that call yield/Subscribe/
//     Register already exist; no new concurrency primitive needed.
//
//   - Hooks are zero-value-friendly: nil = "not set" = baseline behavior
//     (all subscribers receive, payload unchanged, no lifecycle work).
//
// This file defines the type aliases, the HookContext, and the panic-
// recovery wrappers. Wiring into the four call sites (events/poll,
// events/stream, events/subscribe, the emit fanout) is η-3 / η-4 / η-5.

import (
	"context"
	"log"
)

// DeliveryMode identifies which delivery path is invoking a hook so
// authors can implement mode-aware behavior (e.g., a noisy push channel
// they want to filter, but allow webhook through unchanged).
//
// The zero value is intentionally unset — a hook firing without a mode
// would indicate a wiring bug, not a third "default" mode.
type DeliveryMode int

const (
	deliveryModeUnset DeliveryMode = iota
	DeliveryModePoll
	DeliveryModePush
	DeliveryModeWebhook
)

// String returns a human-readable name for the delivery mode.
func (m DeliveryMode) String() string {
	switch m {
	case DeliveryModePoll:
		return "poll"
	case DeliveryModePush:
		return "push"
	case DeliveryModeWebhook:
		return "webhook"
	default:
		return "unset"
	}
}

// HookContext carries per-invocation state into the author's hook
// callbacks: stdlib context for cancellation, plus the pieces of
// subscription identity the author would otherwise have to thread by
// hand (principal, subscription id, delivery mode).
//
// Why an interface instead of a struct: the SDK is the only producer
// — authors only consume HookContext — so an interface keeps the
// surface narrow (4 read-only methods, no zero-value footguns from
// uninitialized exported fields) and lets us add fields on the impl
// without breaking source compatibility for callers that already
// type-asserted on a stable method set.
//
// Why not reuse core.MethodContext: that type is shaped around handler
// invocation (carries id, request method, send-side Notify) — none of
// which makes sense per-emit-fanout-iteration. See
// docs/EVENTS_ETA_PLAN.md Q2.
type HookContext interface {
	// Context returns the underlying stdlib context. Authors honor
	// cancellation / deadlines through this when doing blocking I/O
	// inside a hook.
	Context() context.Context

	// Principal returns the resolved subscription principal (post-
	// UnsafeAnonymousPrincipal fallback). For poll, this matches the
	// principal slot of the lease key.
	Principal() string

	// SubscriptionID returns the server-derived sub_<base64> handle
	// for webhook and push subscriptions. Empty string for poll —
	// poll subscriptions are identified by lease tuple, not by id.
	SubscriptionID() string

	// Mode reports which delivery path is invoking the hook.
	Mode() DeliveryMode
}

// hookContext is the package-internal HookContext implementation. The
// type is unexported so authors can't accidentally construct one out
// of context — they receive HookContext from the SDK and pass it
// around as a value.
type hookContext struct {
	ctx            context.Context
	principal      string
	subscriptionID string
	mode           DeliveryMode
}

func (h *hookContext) Context() context.Context {
	if h == nil || h.ctx == nil {
		return context.Background()
	}
	return h.ctx
}

func (h *hookContext) Principal() string {
	if h == nil {
		return ""
	}
	return h.principal
}

func (h *hookContext) SubscriptionID() string {
	if h == nil {
		return ""
	}
	return h.subscriptionID
}

func (h *hookContext) Mode() DeliveryMode {
	if h == nil {
		return deliveryModeUnset
	}
	return h.mode
}

// newHookContext is the package-internal constructor used by the
// wiring sub-PRs (η-3 / η-4) when invoking the safe* wrappers.
func newHookContext(ctx context.Context, principal, subID string, mode DeliveryMode) HookContext {
	return &hookContext{ctx: ctx, principal: principal, subscriptionID: subID, mode: mode}
}

// MatchFunc decides whether an emitted event should reach the
// subscriber identified by ctx + params. Return true to deliver, false
// to skip. nil MatchFunc on EventDef means "deliver to all".
//
// Python original (Idiom 3, spec L633-644):
//
//	@staticmethod
//	def match(ctx: Context, event: Event, params: dict) -> bool:
//	    sev = params.get("severity")
//	    return sev is None or event.data["severity"] == sev
//
// Go translation: sync function (spec's `async` collapses to plain
// Go funcs per Idiom 2 / Q3); per-subscription parameters move from
// the static-method receiver onto a HookContext argument (Q2). Stored
// as a field on EventDef rather than a separate registration call —
// hooks are per-event-type, EventDef is per-event-type, so they live
// together (Q1).
//
// Spec §"Server SDK Guidance" L623-629. Hot path: invoked once per
// (event, subscriber) pair on broadcast emit; authors should avoid
// I/O. Use HookContext.Context() for cancellation if a downstream
// lookup needs it.
type MatchFunc func(ctx HookContext, event Event, params map[string]any) bool

// TransformFunc shapes the event for one subscriber.
//
// Python original (Idiom 3, spec L633-644):
//
//	@staticmethod
//	def transform(ctx: Context, event: Event, params: dict) -> Event:
//	    if params.get("redact_pii"):
//	        return event.replace(data={**event.data, "reporter": None})
//	    return event
//
// Go translation: sync function with a (Event, bool) return instead
// of plain Event. The `bool` says "I modified the event; re-marshal
// the body for this subscriber." Returning `(orig, false)` is the
// passthrough path — wiring code skips the re-marshal / re-sign work.
// This is the cost Python authors don't see (immutable-update via
// `event.replace(...)` looks free at the syntax level but allocates a
// new dict every time); Go forces it visible, and the bool gives
// authors a way to opt out per-subscriber. See Idiom 4 + Q8.
//
// nil TransformFunc on EventDef means "passthrough; no per-subscriber
// re-marshal".
type TransformFunc func(ctx HookContext, event Event, params map[string]any) (Event, bool)

// SubscribeFunc fires once per subscription lifecycle on initial
// subscribe / first poll.
//
// Python original (Idiom 5, spec L694-697):
//
//	@server.on_subscribe("slack.message")
//	async def on_subscribe(context, params, subscription_id):
//	    """Set up upstream listener for this subscription's params."""
//	    await slack.join_channel(params["channel"])
//
// Go translation: sync function returning error so the author can
// refuse provisioning (e.g., upstream API quota exceeded) — the
// closest analog to a Python coroutine raising. The Python signature
// has `subscription_id` as a third positional argument; Go folds it
// onto HookContext.SubscriptionID() so the call site stays uniform
// across delivery modes (poll has no sub id and the field is empty
// — see Q4). Stored as a field on EventDef rather than a separate
// `@server.on_subscribe(name)` decorator (Q1 — same answer as match/
// transform, since it's still per-event-type state).
//
// Returning a non-nil error fails the subscribe call: the SDK
// surfaces it as -32013 TooManySubscriptions today (η-6 may add a
// dedicated SubscribeFailed code if a real use case appears). The
// SDK does NOT roll back webhook registration / push stream open on
// hook failure; authors that need rollback should arrange it inside
// the hook before returning the error.
type SubscribeFunc func(ctx HookContext, params map[string]any) error

// UnsubscribeFunc fires once per subscription lifecycle on actual
// removal — webhook Unregister / TTL prune / PostTerminated, stream
// close, poll-lease expiry.
//
// Python original (Idiom 5, spec L699-702):
//
//	@server.on_unsubscribe("slack.message")
//	async def on_unsubscribe(context, params, subscription_id):
//	    """Tear down upstream listener."""
//	    await slack.leave_channel(params["channel"])
//
// Go translation: sync function with no return — teardown is
// notification-style. Same subscription_id-onto-HookContext folding
// as SubscribeFunc. Does NOT fire on the suspend transition (Active
// true→false): the subscription is paused, not removed; a subsequent
// refresh reactivates it without re-firing on_subscribe (Q4).
//
// Failures inside the hook are logged + swallowed. Authors that need
// failure visibility should surface it through their own logging /
// metrics from inside the hook.
type UnsubscribeFunc func(ctx HookContext, params map[string]any)

// safeMatch invokes a MatchFunc with panic recovery. A nil match means
// "deliver to all" (returns true). A panicking match logs and returns
// false — a buggy filter shouldn't accidentally fan out everything.
func safeMatch(fn MatchFunc, ctx HookContext, event Event, params map[string]any) (matched bool) {
	if fn == nil {
		return true
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[events] match hook panic for event %q (sub=%q mode=%s): %v",
				event.Name, ctx.SubscriptionID(), ctx.Mode(), r)
			matched = false
		}
	}()
	return fn(ctx, event, params)
}

// safeTransform invokes a TransformFunc with panic recovery. A nil
// transform is passthrough (returns the original event, modified=false).
// A panicking transform logs and returns the original event with
// modified=false — fall back to passthrough rather than dropping the
// event entirely.
func safeTransform(fn TransformFunc, ctx HookContext, event Event, params map[string]any) (out Event, modified bool) {
	if fn == nil {
		return event, false
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[events] transform hook panic for event %q (sub=%q mode=%s): %v",
				event.Name, ctx.SubscriptionID(), ctx.Mode(), r)
			out = event
			modified = false
		}
	}()
	return fn(ctx, event, params)
}

// safeOnSubscribe invokes a SubscribeFunc with panic recovery. A nil
// hook is a no-op (returns nil). A panicking hook logs and returns nil
// — the subscription proceeds; the spec is non-normative here and a
// crash inside the author's lifecycle hook shouldn't take down the
// caller's subscribe attempt.
func safeOnSubscribe(fn SubscribeFunc, ctx HookContext, params map[string]any) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[events] on_subscribe hook panic (sub=%q mode=%s): %v",
				ctx.SubscriptionID(), ctx.Mode(), r)
			err = nil
		}
	}()
	return fn(ctx, params)
}

// safeOnUnsubscribe invokes an UnsubscribeFunc with panic recovery. A
// nil hook is a no-op. A panicking hook logs and is swallowed —
// teardown is best-effort.
func safeOnUnsubscribe(fn UnsubscribeFunc, ctx HookContext, params map[string]any) {
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[events] on_unsubscribe hook panic (sub=%q mode=%s): %v",
				ctx.SubscriptionID(), ctx.Mode(), r)
		}
	}()
	fn(ctx, params)
}
