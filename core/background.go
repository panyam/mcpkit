package core

import "context"

// DetachStrategy is a function that creates a background-safe context from
// a request-scoped context. The server registers a strategy that replaces
// transport-scoped functions (like requestFunc) with session-level equivalents
// that remain valid after the original HTTP request completes.
type DetachStrategy func(ctx context.Context) context.Context

type detachStrategyKey struct{}

// SetDetachStrategy registers a background detach strategy on the context.
// Called by the server's dispatch layer to provide transport-aware detachment.
func SetDetachStrategy(ctx context.Context, strategy DetachStrategy) context.Context {
	return context.WithValue(ctx, detachStrategyKey{}, strategy)
}

// DetachForBackground returns a context suitable for background goroutines.
// It preserves session state (notifications, claims, capabilities) but
// replaces transport-scoped functions with session-level equivalents.
//
// If the server registered a detach strategy (via SetDetachStrategy), it is
// used. Otherwise, falls back to context.WithoutCancel (preserves values,
// detaches cancellation).
//
// Use this instead of context.WithoutCancel when spawning goroutines that
// need to send server-to-client requests (elicitation, sampling) after the
// original HTTP request has returned.
func DetachForBackground(ctx context.Context) context.Context {
	if strategy, ok := ctx.Value(detachStrategyKey{}).(DetachStrategy); ok {
		return strategy(ctx)
	}
	return context.WithoutCancel(ctx)
}

// ReplaceSessionRequestFunc returns a new context with the session's
// requestFunc replaced. Used by detach strategies to swap the dead
// POST-scoped requestFunc with a live session-level one.
//
// No-op if the context has no session.
func ReplaceSessionRequestFunc(ctx context.Context, fn RequestFunc) context.Context {
	sc := sessionFromContext(ctx)
	if sc == nil {
		return ctx
	}
	newSC := *sc
	newSC.request = fn
	return context.WithValue(ctx, sessionCtxKey, &newSC)
}

// ReplaceSessionNotifyFunc returns a new context with the session's
// notifyFunc replaced. Used by detach strategies to swap the dead
// POST-scoped notifyFunc with a live session-level one.
//
// No-op if the context has no session.
func ReplaceSessionNotifyFunc(ctx context.Context, fn NotifyFunc) context.Context {
	sc := sessionFromContext(ctx)
	if sc == nil {
		return ctx
	}
	newSC := *sc
	newSC.notify = fn
	return context.WithValue(ctx, sessionCtxKey, &newSC)
}

// WrapSessionNotifyFunc replaces the session's notifyFunc with the result of
// applying wrap to the current one. Returns ctx unchanged when no session is
// attached or wrap is nil. Provided so cross-cutting concerns (trace context
// injection, audit logging) can wrap notify without re-implementing the
// sessionCtx clone dance — symmetric with WrapSessionRequestFunc.
//
// Common usage from middleware that has the request ctx:
//
//	ctx = core.WrapSessionNotifyFunc(ctx, func(orig core.NotifyFunc) core.NotifyFunc {
//	    return func(method string, params any) {
//	        // mutate params or method, then forward
//	        orig(method, params)
//	    }
//	})
func WrapSessionNotifyFunc(ctx context.Context, wrap func(NotifyFunc) NotifyFunc) context.Context {
	if wrap == nil {
		return ctx
	}
	sc := sessionFromContext(ctx)
	if sc == nil || sc.notify == nil {
		return ctx
	}
	return ReplaceSessionNotifyFunc(ctx, wrap(sc.notify))
}

// WrapSessionRequestFunc replaces the session's requestFunc with the result of
// applying wrap to the current one. Returns ctx unchanged when no session is
// attached, the session has no requestFunc (e.g., transports that do not
// support server-to-client requests), or wrap is nil. Symmetric with
// WrapSessionNotifyFunc.
func WrapSessionRequestFunc(ctx context.Context, wrap func(RequestFunc) RequestFunc) context.Context {
	if wrap == nil {
		return ctx
	}
	sc := sessionFromContext(ctx)
	if sc == nil || sc.request == nil {
		return ctx
	}
	return ReplaceSessionRequestFunc(ctx, wrap(sc.request))
}

// ApplySessionNotifyFilter wraps the session's current notifyFunc with a
// filter that silently drops notifications whose method matches any entry in
// dropMethods, forwarding everything else to the inner notifyFunc. Used by
// execution surfaces that need to suppress notification kinds disallowed by
// their spec — for example, SEP-2663 forbids notifications/progress and
// notifications/message on tasks, so the v2 task goroutine applies this filter
// before invoking the inner tool handler.
//
// No-op if the context has no session, has no notifyFunc, or dropMethods is
// empty.
func ApplySessionNotifyFilter(ctx context.Context, dropMethods ...string) context.Context {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.notify == nil || len(dropMethods) == 0 {
		return ctx
	}
	inner := sc.notify
	drop := make(map[string]struct{}, len(dropMethods))
	for _, m := range dropMethods {
		drop[m] = struct{}{}
	}
	return ReplaceSessionNotifyFunc(ctx, func(method string, params any) {
		if _, blocked := drop[method]; blocked {
			return
		}
		inner(method, params)
	})
}
