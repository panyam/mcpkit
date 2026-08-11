package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// ToolMiddleware wraps the dispatch of one tool call. It is the single
// interception seam: permission gates, argument redaction, rate and budget
// limits, blocklists, result caching, retries, audit trails, and marking
// untrusted tool output are all middleware rather than separate mechanisms.
//
// This is deliberately not a lifecycle hook, and mcpkit does not have those.
// Watching what a turn does is the event stream's job: the Runner already
// emits EventToolBegin, EventToolEnd, EventToolError, EventToolDenied,
// EventToolCancelled, and EventToolUnavailable, so every outcome shape is
// observable without registering anything here. Reach for middleware only to
// *change* behaviour — to alter what runs, what comes back, or whether the
// call happens at all. A middleware that merely observes should be an event
// subscriber instead.
//
// The shape is the familiar wrapping one, matching client.Client's own call
// middleware: receive the call, do work, and invoke next to continue. What
// you do around that invocation is the whole vocabulary.
//
//   - Rewrite arguments: edit info.Call.Args, then call next.
//   - Transform the result: call next, then edit what it returned.
//   - Deny: return DenyTool(reason) without calling next.
//   - Serve from cache: return a result without calling next.
//   - Retry or time the call: invoke next more than once, or measure around it.
//
// Order is RunnerConfig.ToolMiddleware order, outermost first: the first entry
// wraps the second, and the last entry wraps the real dispatch. A permission
// gate therefore belongs *last*, where it sees the arguments every earlier
// entry has already rewritten and nothing can edit them behind its back.
// agent/host appends TieredApproval last for exactly that reason.
//
// A middleware that does not call next has decided the call, and nothing
// further down the chain runs. Implementations must be safe for concurrent
// use, because a turn dispatches parallel tool calls through the same chain.
type ToolMiddleware func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error)

// ToolCallFunc dispatches a tool call. Middleware receives one as next: the
// rest of the chain, ending in the real ToolSource.Call.
type ToolCallFunc func(ctx context.Context, info ToolCallInfo) (*core.ToolResult, error)

// ToolCallInfo is the call travelling down the chain. Middleware may modify
// it before passing it on; the copy that reaches the innermost dispatch is
// what actually executes.
type ToolCallInfo struct {
	// Step is the turn's model-call number this dispatch belongs to,
	// counting from one.
	Step int

	// Call is the tool invocation: name, provider-assigned ID, and the
	// arguments as rewritten by any earlier middleware rather than as the
	// model produced them. Args is core.RawJSON per A5, so a middleware can
	// Bind for typed inspection without a second parse.
	Call ToolCall

	// Scope is where in the run this call sits: the agent ancestry, the
	// remaining sub-agent call budget, and the turn tree's usage against its
	// caps. Populated by the Runner at dispatch; see RunScope.
	//
	// A middleware needs it to behave correctly at depth — a checkpoint that
	// ignores it snapshots once per nested call, and a budget-aware one
	// cannot back off before exhaustion.
	Scope RunScope

	// ReadOnly reflects the tool's readOnlyHint annotation, false when the
	// tool declares none. It is a hint from the server, not a guarantee that
	// the call is free of side effects.
	ReadOnly bool

	// Destructive reports whether the call's effect is irreversible or hard
	// to undo, from the tool's destructiveHint annotation. It is a hint from
	// the server, not a guarantee.
	//
	// True when the tool declares nothing, because the spec's default for an
	// absent destructiveHint on a writing tool is destructive. That inverts
	// the Go zero value, so a hand-built ToolCallInfo{} reads as
	// non-destructive: only a value the Runner populated carries a meaningful
	// answer. Normalized against ReadOnly, which the spec says wins — a
	// read-only tool reports false whatever it annotated.
	Destructive bool

	// Idempotent reports whether repeating the call with the same arguments
	// has no additional effect, from the tool's idempotentHint annotation. It
	// is a hint from the server, not a guarantee.
	//
	// False when the tool declares nothing, matching the spec default, so the
	// zero value is the conservative reading here rather than the inverted
	// one. Read-only tools report true.
	Idempotent bool
}

// ToolDeniedError is how a middleware refuses a call. The Runner unwraps it
// specially: the model is told the call was not permitted, surfaces get
// EventToolDenied, and the turn continues. It is not a turn abort and not a
// tool failure, so the model can choose differently rather than seeing an
// error it cannot act on.
type ToolDeniedError struct{ Reason string }

func (e *ToolDeniedError) Error() string {
	if e.Reason == "" {
		return "tool call denied"
	}
	return "tool call denied: " + e.Reason
}

// DenyTool builds the error a middleware returns to refuse a call. Return it
// without calling next; an empty reason gets a default.
func DenyTool(reason string) error { return &ToolDeniedError{Reason: reason} }

// deniedReason reports whether err is a denial and the reason to surface.
func deniedReason(err error) (string, bool) {
	var d *ToolDeniedError
	if !errors.As(err, &d) {
		return "", false
	}
	if d.Reason == "" {
		return "denied by policy", true
	}
	return d.Reason, true
}

// chainToolMiddleware wraps base so mw[0] is outermost and the last entry
// wraps base itself.
func chainToolMiddleware(mw []ToolMiddleware, base ToolCallFunc) ToolCallFunc {
	for i := len(mw) - 1; i >= 0; i-- {
		next, m := base, mw[i]
		base = func(ctx context.Context, info ToolCallInfo) (*core.ToolResult, error) {
			return m(ctx, info, next)
		}
	}
	return base
}

// BeforeToolCall adapts a plain inspect-or-rewrite function into middleware,
// for the common case that does not need to see the result. Returning an
// error refuses the call (use DenyTool for an intentional refusal); returning
// nil continues with whatever the function left in info.
func BeforeToolCall(fn func(ctx context.Context, info *ToolCallInfo) error) ToolMiddleware {
	return func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		if err := fn(ctx, &info); err != nil {
			return nil, err
		}
		return next(ctx, info)
	}
}

// AfterToolCall adapts a plain result-transforming function into middleware,
// the shape a redaction or untrusted-output marker takes. fn runs only when
// the call succeeded; a failed or denied call has no result to transform and
// its error passes straight through.
func AfterToolCall(fn func(ctx context.Context, info ToolCallInfo, res *core.ToolResult) (*core.ToolResult, error)) ToolMiddleware {
	return func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		res, err := next(ctx, info)
		if err != nil {
			return nil, err
		}
		out, ferr := fn(ctx, info, res)
		if ferr != nil {
			return nil, fmt.Errorf("tool middleware: %w", ferr)
		}
		if out != nil {
			return out, nil
		}
		return res, nil
	}
}
