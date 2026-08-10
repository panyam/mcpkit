package agent

import (
	"context"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// Approval is one ToolMiddleware among others rather than a mechanism of its
// own. A permission gate is middleware that declines to call next; that it
// asks a human first is an implementation detail, not a different kind of
// interception. See ToolMiddleware for the ordering contract, and
// RunnerConfig.ToolMiddleware for why a gate belongs last in the chain.

// ApprovalMode is the default disposition TieredApproval applies to a call
// that no per-tool rule covers.
type ApprovalMode int

const (
	// ModeAlwaysAsk asks for every uncovered call. The safe default.
	ModeAlwaysAsk ApprovalMode = iota
	// ModeReadOnlyAuto auto-allows calls whose tool declares readOnlyHint
	// and asks for the rest. The "read-only → auto-edit" rung of the ladder.
	ModeReadOnlyAuto
	// ModeReversibleAuto auto-allows a call that reads, and a call that
	// writes something the tool declares it can undo, asking only when the
	// effect is irreversible. The rung above ModeReadOnlyAuto: it separates
	// "does this write?" from "can this be taken back?", so an editing tool
	// stops prompting while a send or a delete still does.
	//
	// It keys on ToolCallInfo.Destructive, so a tool that annotates nothing
	// is asked about rather than assumed reversible. Against servers that
	// skip destructiveHint entirely this behaves exactly like ModeAlwaysAsk,
	// which is the intended failure direction.
	ModeReversibleAuto
	// ModeAlwaysAllow runs every uncovered call without asking (full-auto /
	// "yolo"). Per-tool Deny rules still apply on top.
	ModeAlwaysAllow
)

// ToolRule is the per-tool override that takes precedence over the mode.
type ToolRule int

const (
	// RuleAsk forces an ask for this tool regardless of mode.
	RuleAsk ToolRule = iota
	// RuleAllow auto-allows this tool regardless of mode.
	RuleAllow
	// RuleDeny refuses this tool outright, without asking.
	RuleDeny
)

// AskFunc presents a yes/no approval prompt and returns the user's choice.
// ElicitationCoordinator.Confirm satisfies it, which is how the "ask" outcome
// reuses the existing FIFO UI seam instead of introducing a second one. A nil
// AskFunc makes every ask resolve to a refusal (fail-closed).
type AskFunc func(ctx context.Context, info ToolCallInfo) (bool, error)

// TieredApproval is the batteries-included permission gate: a default mode, a
// map of per-tool rules that override it, an optional ask seam, and an
// optional session-scoped cache that remembers a tool the user approved so
// later calls to it skip the prompt. Safe for concurrent use by the Runner's
// parallel dispatch.
type TieredApproval struct {
	mode     ApprovalMode
	rules    map[string]ToolRule
	ask      AskFunc
	remember bool

	mu         sync.Mutex
	remembered map[string]bool
}

// TieredOption configures a TieredApproval.
type TieredOption func(*TieredApproval)

// WithDefaultMode sets the disposition for calls no per-tool rule covers.
// Without it, the mode is ModeAlwaysAsk.
func WithDefaultMode(m ApprovalMode) TieredOption {
	return func(t *TieredApproval) { t.mode = m }
}

// WithToolRule pins a per-tool override that wins over the mode. Call it once
// per tool; a later rule for the same name replaces an earlier one.
func WithToolRule(tool string, rule ToolRule) TieredOption {
	return func(t *TieredApproval) { t.rules[tool] = rule }
}

// WithAsk supplies the seam that presents an approval prompt. Pass
// coord.Confirm to route asks through the shared ElicitationCoordinator.
func WithAsk(ask AskFunc) TieredOption {
	return func(t *TieredApproval) { t.ask = ask }
}

// WithRememberApprovals turns on the session cache: once the user approves a
// tool through an ask, subsequent calls to that same tool auto-allow for the
// life of this policy. A denial is never remembered.
func WithRememberApprovals(remember bool) TieredOption {
	return func(t *TieredApproval) { t.remember = remember }
}

// NewTieredApproval builds a TieredApproval. With no options it asks for every
// call and, lacking an AskFunc, refuses them all (fail-closed) — supply
// WithAsk to make asking meaningful.
func NewTieredApproval(opts ...TieredOption) *TieredApproval {
	t := &TieredApproval{
		mode:       ModeAlwaysAsk,
		rules:      map[string]ToolRule{},
		remembered: map[string]bool{},
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// SetDefaultMode changes the default mode at runtime (the seam a host's
// "/approve <mode>" or "/yolo" command uses). Concurrency-safe against calls
// in flight; per-tool rules and the remember-cache are unaffected.
func (t *TieredApproval) SetDefaultMode(m ApprovalMode) {
	t.mu.Lock()
	t.mode = m
	t.mu.Unlock()
}

// DefaultMode reports the current default mode.
func (t *TieredApproval) DefaultMode() ApprovalMode {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mode
}

// BeforeTool applies, in order: a remembered approval, a per-tool rule, then
// the default mode. An ask that the user accepts is remembered when the cache
// is on. A refusal (rule Deny, an ask returning false, or an ask with no
// AskFunc wired) denies with a Reason; the Runner feeds that back to the model
// and continues the turn.
//
// It never rewrites arguments. A gate decides, it does not edit, and leaving
// the rewrite to other hooks is what lets this one run last and still see
// exactly what will execute.
func (t *TieredApproval) WrapToolCall(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
	t.mu.Lock()
	mode := t.mode
	remembered := t.remember && t.remembered[info.Call.Name]
	t.mu.Unlock()
	if remembered {
		return next(ctx, info)
	}

	if rule, ok := t.rules[info.Call.Name]; ok {
		switch rule {
		case RuleAllow:
			return next(ctx, info)
		case RuleDeny:
			return nil, DenyTool("denied by approval policy")
		case RuleAsk:
			return t.doAsk(ctx, info, next)
		}
	}

	switch mode {
	case ModeAlwaysAllow:
		return next(ctx, info)
	case ModeReadOnlyAuto:
		if info.ReadOnly {
			return next(ctx, info)
		}
		return t.doAsk(ctx, info, next)
	case ModeReversibleAuto:
		if info.ReadOnly || !info.Destructive {
			return next(ctx, info)
		}
		return t.doAsk(ctx, info, next)
	default: // ModeAlwaysAsk
		return t.doAsk(ctx, info, next)
	}
}

func (t *TieredApproval) doAsk(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
	if t.ask == nil {
		return nil, DenyTool("no approval UI available")
	}
	ok, err := t.ask(ctx, info)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, DenyTool("declined by user")
	}
	if t.remember {
		t.mu.Lock()
		t.remembered[info.Call.Name] = true
		t.mu.Unlock()
	}
	return next(ctx, info)
}
