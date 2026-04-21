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
