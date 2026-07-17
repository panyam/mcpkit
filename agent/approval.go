package agent

import (
	"context"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// ApprovalPolicy gates each tool call before it runs. The Runner consults it
// in callTool, after argument binding and before ToolSource.Call; a nil
// policy on RunnerConfig means every call runs (the pre-approval behavior).
//
// Approve may block to ask the user. It returns a decision, never a turn
// abort: a refusal is fed back to the model as a tool result and the loop
// continues, so an error from Approve is reserved for the policy itself
// failing (for example, the ask UI could not present the prompt).
type ApprovalPolicy interface {
	Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)
}

// ApprovalRequest describes the call a policy is deciding on. Args is the
// model-supplied arguments object (core.RawJSON per A5, so a policy can Bind
// for typed inspection without a second parse). ReadOnly reflects the tool's
// readOnlyHint annotation, the signal the read-only-auto tier keys on; it is
// false when the tool declares no such hint.
type ApprovalRequest struct {
	ToolName string
	Args     core.RawJSON
	ReadOnly bool
}

// ApprovalDecision is a policy's verdict. Allowed true runs the call. On a
// refusal, Reason is surfaced to the model (as the tool result text) and to
// surfaces (on the tool-denied event); an empty Reason gets a default.
type ApprovalDecision struct {
	Allowed bool
	Reason  string
}

// ApprovalMode is the default disposition TieredApproval applies to a call
// that no per-tool rule covers.
type ApprovalMode int

const (
	// ModeAlwaysAsk asks for every uncovered call. The safe default.
	ModeAlwaysAsk ApprovalMode = iota
	// ModeReadOnlyAuto auto-allows calls whose tool declares readOnlyHint
	// and asks for the rest. The "read-only → auto-edit" rung of the ladder.
	ModeReadOnlyAuto
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
type AskFunc func(ctx context.Context, req ApprovalRequest) (bool, error)

// TieredApproval is the batteries-included ApprovalPolicy: a default mode, a
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

// Approve applies, in order: a remembered approval, a per-tool rule, then the
// default mode. An ask that the user accepts is remembered when the cache is
// on. A refusal (rule Deny, an ask returning false, or an ask with no AskFunc
// wired) yields Allowed:false with a Reason; the Runner feeds that back to the
// model and continues the turn.
func (t *TieredApproval) Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
	if t.remember {
		t.mu.Lock()
		ok := t.remembered[req.ToolName]
		t.mu.Unlock()
		if ok {
			return ApprovalDecision{Allowed: true}, nil
		}
	}

	if rule, ok := t.rules[req.ToolName]; ok {
		switch rule {
		case RuleAllow:
			return ApprovalDecision{Allowed: true}, nil
		case RuleDeny:
			return ApprovalDecision{Reason: "denied by approval policy"}, nil
		case RuleAsk:
			return t.doAsk(ctx, req)
		}
	}

	switch t.mode {
	case ModeAlwaysAllow:
		return ApprovalDecision{Allowed: true}, nil
	case ModeReadOnlyAuto:
		if req.ReadOnly {
			return ApprovalDecision{Allowed: true}, nil
		}
		return t.doAsk(ctx, req)
	default: // ModeAlwaysAsk
		return t.doAsk(ctx, req)
	}
}

func (t *TieredApproval) doAsk(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
	if t.ask == nil {
		return ApprovalDecision{Reason: "no approval UI available"}, nil
	}
	ok, err := t.ask(ctx, req)
	if err != nil {
		return ApprovalDecision{}, err
	}
	if !ok {
		return ApprovalDecision{Reason: "declined by user"}, nil
	}
	if t.remember {
		t.mu.Lock()
		t.remembered[req.ToolName] = true
		t.mu.Unlock()
	}
	return ApprovalDecision{Allowed: true}, nil
}
