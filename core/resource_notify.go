package core

import "context"

// NotifyResourceUpdated sends a notifications/resources/updated to all
// sessions subscribed to the given resource URI. This is the
// handler-facing wrapper around Server.NotifyResourceUpdated — it
// routes through the subscription registry to fan out across sessions,
// not just the caller's session.
//
// No-op if subscriptions are not enabled or no session context is present.
// Uses the existing MCP spec subscription mechanism (exact URI match);
// no wildcard or pattern extensions.
//
// Usage in a tool handler:
//
//	func updateWidget(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
//	    db.UpdateWidget(args.ID, args.Name)
//	    core.NotifyResourceUpdated(ctx, "widgets/" + args.ID)
//	    return core.TextResult("updated"), nil
//	}
func NotifyResourceUpdated(ctx context.Context, uri string) {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.notifyResourceUpdated == nil {
		return
	}
	sc.notifyResourceUpdated(uri)
}

// SetNotifyResourceUpdated installs the resource-updated fan-out function
// on the session context. Exported for the server dispatch layer to wire
// the subscription registry's notify method. Must be called AFTER
// ContextWithSession.
func SetNotifyResourceUpdated(ctx context.Context, fn func(uri string)) context.Context {
	if sc := sessionFromContext(ctx); sc != nil {
		sc.notifyResourceUpdated = fn
	}
	return ctx
}
