package core

import "context"

// NotifyResourceUpdated sends a notifications/resources/updated to all
// sessions subscribed to the given resource URI. This is the
// handler-facing wrapper around Server.NotifyResourceUpdated — it
// routes through the subscription registry to fan out across sessions,
// not just the caller's session.
//
// No-op if subscriptions are not enabled or the dispatch layer installed no
// notifier (SetNotifyResourceUpdated). Works on both wires.
// Uses the existing MCP spec subscription mechanism (exact URI match);
// no wildcard or pattern extensions.
//
// Usage in a tool handler:
//
//	func updateWidget(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
//	    db.UpdateWidget(args.ID, args.Name)
//	    core.NotifyResourceUpdated(ctx, "widgets/" + args.ID)
//	    return core.TextResult("updated"), nil
//	}
func NotifyResourceUpdated(ctx context.Context, uri string) {
	fn, _ := ctx.Value(statelessResourceNotifierCtxKey).(func(uri string))
	if sc := sessionFromContext(ctx); sc != nil {
		fn = sc.notifyResourceUpdated
	}
	if fn != nil {
		fn(uri)
	}
}

// SetNotifyResourceUpdated installs the resource-updated fan-out function
// on the session context. Exported for the server dispatch layer to wire
// the subscription registry's notify method. Call it after
// ContextWithSession; with no session in ctx (the stateless wire) the
// function rides on the returned ctx instead.
func SetNotifyResourceUpdated(ctx context.Context, fn func(uri string)) context.Context {
	if sc := sessionFromContext(ctx); sc != nil {
		sc.notifyResourceUpdated = fn
		return ctx
	}
	return context.WithValue(ctx, statelessResourceNotifierCtxKey, fn)
}
