package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// DefaultMaxSteps bounds a turn when RunnerConfig.MaxSteps is zero. Eight
// model calls is generous for real workflows; hitting it usually means the
// model is looping on a failing tool.
const DefaultMaxSteps = 8

// ErrMaxSteps is returned (wrapped) by Run when the model keeps requesting
// tool calls past the step cap. Check with errors.Is.
var ErrMaxSteps = errors.New("agent: max steps exceeded")

// ErrNotAvailableNow is returned by a ToolSource.Call when the tool exists but
// its backing server is unreachable right now (as opposed to the tool failing).
// The Runner treats it as a NON-FATAL miss: it emits EventToolUnavailable and
// feeds the wrapped error's message back to the model, and the turn continues.
// Wrap it with a descriptive message (fmt.Errorf("%w: ...", ErrNotAvailableNow,
// ...)) so the model learns which server and can retry, route around it, or
// tell the user. See docs/AGENT_SERVER_STATE.md.
var ErrNotAvailableNow = errors.New("agent: tool source not available now")

// RunnerConfig assembles a Runner.
type RunnerConfig struct {
	// Provider is the LLM. Required.
	Provider Provider

	// Tools is the tool surface offered to the model. Optional: nil means
	// the model is offered no tools and any hallucinated call fails back
	// into the conversation.
	Tools ToolSource

	// Instructions is the system prompt sent on every step.
	Instructions string

	// InstructionsFunc, when non-nil, is called once at the top of each turn to
	// compute that turn's system prompt, overriding the static Instructions. It
	// lets the prompt track dynamic state — the set of currently-connected
	// servers whose eager skills belong in the prompt, the date, injected
	// context — instead of being frozen at construction. Recomputed per turn
	// (not per step), so the prompt is stable within a turn and a provider
	// cache only breaks when the value actually changes.
	//
	// It is per-Runner: each Runner is built from its own RunnerConfig, so a
	// sub-agent's Runner has its own InstructionsFunc (or none) — different
	// runners can produce different prompts. The func captures whatever context
	// it needs (e.g. the host's set of connected servers, its own tool view) via
	// closure; the Runner is deliberately NOT passed, which would be circular
	// (the Runner holds this config). The ctx is for cancellation/deadline only.
	InstructionsFunc func(context.Context) string

	// MaxSteps caps model calls per turn. Zero means DefaultMaxSteps.
	MaxSteps int

	// TreeBudget caps aggregate steps/tokens across the whole sub-agent tree
	// (parent + sub-agents + fan-out members + handoff rounds), complementing
	// the per-Runner MaxSteps. The Runner installs it on ctx at the top of a
	// turn ONLY if the ctx does not already carry one, so a top-level Runner's
	// budget is inherited and shared by every child run (do not set it on
	// sub-agent Runner configs — they inherit it). Zero (both dimensions) is no
	// budget. Equivalent to calling WithTreeBudget on the turn ctx yourself.
	TreeBudget TreeBudget

	// TracerProvider opts the Runner into SEP 414 span emission:
	// agent.turn per Run, agent.step per model call, agent.tool per
	// dispatch, with ctx threading so client-side dispatch spans (and
	// through them server spans) stitch as children. Nil or
	// core.NoopTracerProvider means zero overhead, the repo-wide pattern.
	TracerProvider core.TracerProvider

	// MeterProvider opts the Runner into OTel metric emission (issue 1023),
	// the metrics sibling of the trace spans: a turn counter + duration
	// histogram, a steps counter, a tokens counter (by direction), and a
	// tool-call counter + duration histogram (by tool and status). Emitted
	// at the same points the spans are. Nil or core.NoopMeterProvider means
	// zero overhead, the same repo-wide pattern as TracerProvider.
	MeterProvider core.MeterProvider

	// Selector, when non-nil, narrows the tools offered to the model each
	// step. It runs on the freshly listed set with the full history, so
	// context-aware routing (keyword, embedding, scored) plugs in here.
	// Selectors must stay pure functions of (history, tools): any cache a
	// selector keeps should key on tool-list content, never on time or
	// notifications, so list-changed invalidation has exactly one source
	// (the ToolSource layer). A selector error aborts the turn: it is a
	// host configuration bug, not something the model can recover from.
	Selector ToolSelector

	// Approval, when non-nil, gates each tool call before it runs: the
	// Runner asks the policy in callTool, after argument binding and before
	// ToolSource.Call. A refusal is fed back to the model as a tool result
	// (a tool-denied event, then the turn continues), never a turn abort.
	// Nil means every call runs, the pre-approval behavior.
	Approval ApprovalPolicy

	// Generation carries the default generation knobs (temperature, token
	// cap, tool-choice bias) for every model call this Runner makes, both
	// the streaming steps and the finalizing Generate of a structured-output
	// turn. TurnRequest.Generation overrides it per turn, field by field.
	//
	// The zero value sends nothing, which is the behavior before these were
	// reachable: the provider's own defaults apply and the request carries
	// no temperature, max_tokens, or tool_choice.
	Generation GenerationParams

	// Compactor, when non-nil, may rewrite the turn's history before the
	// first model call — the head summarized, a recent tail kept verbatim —
	// to keep a long conversation under a context budget. The Runner calls
	// it on its own clone of the history, so Run stays stateless over the
	// history it is handed; a no-op (Compactor returns the input unchanged)
	// emits nothing, a real compaction emits EventCompaction. A Compactor
	// error aborts the turn (a misconfiguration or summarizer-provider
	// outage, not something the model can recover from), mirroring Selector.
	// Nil means history is sent verbatim. Mid-turn compaction (a single turn
	// that itself grows past the budget) is a follow-up; this fires once,
	// pre-loop.
	Compactor Compactor

	// Interruptible opts this Runner's turn into breaking the fan-out join
	// barrier when a child raises an upward Signal mid-flight (issue 1167, piece
	// C of the 1036 control axis). Default false keeps the turn a pure
	// fan-out-then-join — the property resume / fork / eval / compaction rely on;
	// only a signal-wired turn should set it. When true, the first barrier-
	// breaking signal a child raises during a dispatch (see shouldBreakOn:
	// escalate always, preempt only when PreemptGrant honors it, custom never)
	// cancels the remaining in-flight calls (they feed back "cancelled by user")
	// and the dispatch returns the partial results, so the turn's step loop
	// re-enters the model to re-plan. With no breaking signal, an interruptible
	// turn still waits for every call, identical to the default. The re-entry
	// ordering is the one bounded-nondeterminism exception to the pure turn;
	// every emitted event still projects 1:1 (A2).
	Interruptible bool

	// SignalPolicy, when non-nil, decides how this Runner (as a parent) reacts
	// to the upward Signals its children raised during a dispatch, read at the
	// join (issue 1165). It runs after the fan-out has joined, so it chooses
	// only whether to abort the turn (SignalAction.AbortTurn -> ErrSignalAbort);
	// the signals are injected into the next step as a RoleSystem note either
	// way, so the parent model sees them. Nil means inject-and-continue. See
	// AbortOnEscalate for the built-in deterministic policy.
	SignalPolicy SignalPolicy

	// PreemptGrant gates whether a child's advisory SignalPreempt breaks the
	// interruptible join barrier (cancelling the other in-flight calls). It is
	// the parent's authority over a claim the child cannot actually verify (a
	// child under A7 isolation cannot know the global goal). Nil (the default)
	// means a preempt never breaks — it is injected like any signal and the
	// parent model decides on re-plan, so a rogue or prompt-injected child
	// cannot unilaterally cancel its siblings. Non-nil is consulted per preempt
	// signal (it may inspect Source/Note to honor only trusted children);
	// returning true grants the preemption. Must be pure and goroutine-safe (it
	// is called from a child's raise). Only consulted when Interruptible is set;
	// it does not gate SignalEscalate, which breaks unconditionally.
	PreemptGrant func(sig Signal) bool

	// ResponseSchema, when set, coerces the turn's final answer into
	// structured output. After the tool loop reaches its terminal
	// no-tool-call text, the Runner makes one additional Generate call with
	// this schema (and no tools) and puts the JSON document on
	// TurnResult.Structured. Tools and a response schema are never sent in
	// the same request (many endpoints forbid it), which is why this is a
	// separate finalizing call rather than a field on the loop's requests.
	// Empty means no structured coercion.
	ResponseSchema core.RawJSON
}

// ToolSelector narrows the model-facing tool set for one step. Returning the
// input slice unchanged (or nil selector) offers everything; returning an
// empty slice offers no tools for that step. Names must be preserved
// verbatim: Call routing still resolves against the underlying ToolSource.
type ToolSelector func(ctx context.Context, history []Message, tools []core.ToolDef) ([]core.ToolDef, error)

// Runner executes turns: the multi-step loop that streams the model,
// dispatches its tool calls, feeds results back, and repeats until the model
// answers in text. Safe for concurrent use; each Run call is an independent
// turn.
type Runner struct {
	cfg     RunnerConfig
	metrics *runnerMetrics
}

// NewRunner validates cfg and returns a Runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agent: RunnerConfig requires a Provider")
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.TracerProvider == nil {
		cfg.TracerProvider = core.NoopTracerProvider{}
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = core.NoopMeterProvider{}
	}
	return &Runner{cfg: cfg, metrics: newRunnerMetrics(cfg.MeterProvider)}, nil
}

// TurnResult is the completed turn. Messages holds exactly the entries the
// turn appended (assistant messages and tool results, in order), so callers
// thread history as append(history, result.Messages...).
type TurnResult struct {
	// Text is the final assistant message of the turn, which is the Text of
	// the last model call. Intermediate steps' text is not concatenated here;
	// read Messages for the full sequence, or the event stream to see text as
	// it arrived.
	Text string `json:"text,omitempty"`

	// Messages is exactly what the turn appended, never the full history.
	// Thread it with append(history, result.Messages...).
	Messages []Message `json:"messages"`

	// Usage sums every model call this Runner made during the turn: each
	// step of the loop plus the finalizing Generate of a structured-output
	// turn. Steps whose provider reported no usage contribute zero, so an
	// under-reporting provider yields an undercount rather than an error.
	//
	// It does not include sub-agents. A child reached through AgentSource,
	// AsyncAgentSource, FanOutSource, Team, or AgentPool runs its own Runner
	// and accounts for its own tokens, so cost accounting over an agent tree
	// has to sum the children separately. TreeBudget is the mechanism that
	// does see the whole tree; this field deliberately does not.
	Usage Usage `json:"usage"`

	// Steps is how many times the loop called the model, counting from one.
	// A turn that answered without tools reports 1. The finalizing Generate
	// of a structured-output turn is not counted here, though its tokens are
	// counted in Usage. Reaching RunnerConfig.MaxSteps returns an error
	// wrapping ErrMaxSteps instead of a result.
	Steps int `json:"steps"`

	// FinishReason is the last model call's finish reason, in the provider's
	// own vocabulary and unmapped. See ProviderResponse.FinishReason.
	FinishReason string `json:"finishReason,omitempty"`

	// Structured is the schema-coerced final answer, present only when
	// RunnerConfig.ResponseSchema was set. It is the JSON document from the
	// finalizing Generate call; Bind it into a typed value. Its Usage is
	// already folded into Usage above. Empty when no schema was configured.
	Structured core.RawJSON `json:"structured,omitempty"`
}

// Control is the turn's steering envelope: surfaces send Controls on
// TurnRequest.Control to steer a turn while it runs. Cancellation is the
// first (currently only) verb, in two modes:
//
//   - Control{} — cancel ALL calls currently in flight, one send. The
//     naive-Esc path: a surface needs no bookkeeping, three in-flight
//     calls die from a single Control.
//   - Control{CallID: id} — cancel exactly one call, identified by the
//     ToolCall.ID the surface saw on that call's tool-begin event. For
//     richer surfaces (a TUI with a row per running call).
//
// Either way the decision stays with the sender: surfaces already hold
// the call inventory via tool-begin/tool-end events, and constraint A4
// keeps decision callbacks out of the loop.
//
// Future steering verbs (pause, budget bumps, mid-turn priority hints)
// extend this struct additively — a Kind discriminator plus verb fields,
// with the zero Kind meaning cancel for compatibility — rather than new
// channels or a handler registry. Mid-turn *content* (a "/btw" note for
// the model) is deliberately not a Control: anything the model should
// see routes through the injection path so it enters history as a
// message, not a side effect.
type Control struct {
	// CallID names the in-flight tool call to cancel; empty cancels
	// every call currently in flight. An ID that is not in flight
	// (already finished, or never dispatched) is a no-op, so racing a
	// call's natural completion is safe.
	CallID string
}

// TurnRequest consolidates RunTurn's inputs so the turn surface can grow
// without breaking signatures (the same C2 shape RunnerConfig uses).
type TurnRequest struct {
	// History is the conversation so far; RunTurn clones it and returns
	// only appended messages, exactly like Run.
	History []Message

	// Emit receives the turn's event stream. Nil is allowed; emit is
	// never called concurrently.
	Emit func(Event)

	// Control, when non-nil, is drained for the whole turn. A Control
	// cancels the targeted call's own context: the call fails fast
	// (ClientSource.Call threads ctx to the wire, so MCP servers see a
	// real cancellation), its result is fed back to the model as
	// "cancelled by user", and the turn continues — unlike cancelling
	// RunTurn's ctx, which aborts the whole turn. Send only while a
	// turn is running: between turns nothing drains the channel, and a
	// buffered cancel-all would hit the next turn's first dispatch.
	Control <-chan Control

	// Generation overrides RunnerConfig.Generation for this turn, field by
	// field: a set field wins, a zero field inherits the config's. Use it
	// for per-turn decisions the config cannot express — forcing a tool call
	// on a proactive turn, capping tokens on one cheap turn, varying
	// temperature across sampled candidates.
	//
	// The zero value changes nothing and the turn runs on the config's
	// defaults.
	Generation GenerationParams
}

// Run executes one turn against history. Events stream to emit (nil is
// allowed); emit is never called concurrently. Tool failures of every kind
// (unknown tool, transport, bad args) are fed back to the model as
// error-marked tool results and the loop continues; only ctx cancellation,
// provider failure, or the step cap abort the turn. The returned error wraps
// ErrMaxSteps when the cap was hit. Run is shorthand for RunTurn without
// mid-turn controls.
func (r *Runner) Run(ctx context.Context, history []Message, emit func(Event)) (*TurnResult, error) {
	return r.RunTurn(ctx, TurnRequest{History: history, Emit: emit})
}

// RunTurn executes one turn with the full request surface: Run's
// contract plus mid-turn Controls (per-call cancellation). See
// TurnRequest for the semantics of each field.
func (r *Runner) RunTurn(ctx context.Context, req TurnRequest) (*TurnResult, error) {
	history, emit := req.History, req.Emit
	if emit == nil {
		emit = func(Event) {}
	}
	gen := r.cfg.Generation.merge(req.Generation)

	var reg *callCancels
	if req.Control != nil {
		reg = &callCancels{cancels: map[string]context.CancelFunc{}}
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			for {
				select {
				case c, ok := <-req.Control:
					if !ok {
						return
					}
					reg.cancel(c.CallID)
				case <-stop:
					return
				}
			}
		}()
	}
	// Install the aggregate tree budget only if one is not already threaded on
	// ctx: the top-level Runner installs it and every sub-agent run inherits the
	// same shared counter, so the totals are aggregate across the tree.
	if !r.cfg.TreeBudget.zero() && treeBudgetFrom(ctx) == nil {
		ctx = WithTreeBudget(ctx, r.cfg.TreeBudget)
	}
	treeBudget := treeBudgetFrom(ctx)

	ctx, turnSpan := r.cfg.TracerProvider.StartSpan(ctx, "agent.turn")
	defer turnSpan.End()
	turnStart := time.Now()
	emit(Event{Kind: EventTurnBegin})

	// The system prompt is resolved once per turn (dynamic when InstructionsFunc
	// is set), then reused across this turn's steps.
	turnInstructions := r.cfg.Instructions
	if r.cfg.InstructionsFunc != nil {
		turnInstructions = r.cfg.InstructionsFunc(ctx)
	}

	msgs := slices.Clone(history)
	if r.cfg.Compactor != nil {
		before := len(msgs)
		compacted, err := r.cfg.Compactor.Compact(ctx, msgs)
		if err != nil {
			return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: compaction: %w", err))
		}
		if len(compacted) != before {
			msgs = compacted
			emit(Event{Kind: EventCompaction, Compaction: &CompactionInfo{Before: before, After: len(msgs)}})
		}
	}
	var added []Message
	var usage Usage

	for step := 1; step <= r.cfg.MaxSteps; step++ {
		// Aggregate tree budget: charge one step (and reject if prior steps
		// already blew the token cap) before spending another model call.
		if !treeBudget.consumeStep() {
			return nil, r.failSpan(emit, turnSpan, fmt.Errorf("%w (aggregate across the agent tree)", ErrTreeBudget))
		}
		stepCtx, stepSpan := r.cfg.TracerProvider.StartSpan(ctx, "agent.step",
			core.Attribute{Key: "agent.step", Value: fmt.Sprint(step)})

		var tools []core.ToolDef
		if r.cfg.Tools != nil {
			var err error
			if tools, err = r.cfg.Tools.Tools(stepCtx); err != nil {
				stepSpan.RecordError(err)
				stepSpan.End()
				return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: listing tools: %w", err))
			}
			if r.cfg.Selector != nil {
				if tools, err = r.cfg.Selector(stepCtx, msgs, tools); err != nil {
					stepSpan.RecordError(err)
					stepSpan.End()
					return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: tool selector: %w", err))
				}
			}
		}
		stepSpan.SetAttribute("agent.tools.offered", fmt.Sprint(len(tools)))

		stepReq := ProviderRequest{
			Instructions: turnInstructions,
			Messages:     msgs,
			Tools:        tools,
		}
		gen.applyTo(&stepReq)
		stream, err := r.cfg.Provider.Stream(stepCtx, stepReq)
		if err != nil {
			stepSpan.RecordError(err)
			stepSpan.End()
			return nil, r.failSpan(emit, turnSpan, err)
		}
		resp, err := consumeStream(stream, step, emit)
		stream.Close()
		if err != nil {
			stepSpan.RecordError(err)
			stepSpan.End()
			return nil, r.failSpan(emit, turnSpan, err)
		}
		if resp.Usage != nil {
			usage.InputTokens += resp.Usage.InputTokens
			usage.OutputTokens += resp.Usage.OutputTokens
			treeBudget.addTokens(resp.Usage.InputTokens + resp.Usage.OutputTokens)
		}

		assistant := Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls}
		msgs = append(msgs, assistant)
		added = append(added, assistant)

		if len(resp.ToolCalls) == 0 {
			stepSpan.End()
			var structured core.RawJSON
			if r.cfg.ResponseSchema.Len() > 0 {
				s, err := r.finalizeStructured(ctx, turnInstructions, msgs, &usage, gen)
				if err != nil {
					return nil, r.failSpan(emit, turnSpan, fmt.Errorf("agent: structured finalize: %w", err))
				}
				structured = s
			}
			result := &TurnResult{
				Text:         resp.Text,
				Messages:     added,
				Usage:        usage,
				Steps:        step,
				FinishReason: resp.FinishReason,
				Structured:   structured,
			}
			turnSpan.SetAttribute("agent.steps", fmt.Sprint(step))
			turnSpan.SetAttribute("agent.finish_reason", resp.FinishReason)
			turnSpan.SetAttribute("agent.tokens.input", fmt.Sprint(usage.InputTokens))
			turnSpan.SetAttribute("agent.tokens.output", fmt.Sprint(usage.OutputTokens))
			r.metrics.turnDone(ctx, step, usage.InputTokens, usage.OutputTokens, resp.FinishReason, time.Since(turnStart))
			emit(Event{Kind: EventTurnEnd, Result: result})
			return result, nil
		}

		toolMsgs, signals := r.dispatch(stepCtx, step, resp.ToolCalls, tools, emit, reg)
		stepSpan.End()
		if err := ctx.Err(); err != nil {
			return nil, r.failSpan(emit, turnSpan, err)
		}
		// Tool results must immediately follow the assistant message that
		// requested them (providers pair RoleTool with the assistant's tool
		// calls), so append them first; a signal note goes after.
		msgs = append(msgs, toolMsgs...)
		added = append(added, toolMsgs...)
		if len(signals) > 0 {
			for i := range signals {
				emit(Event{Kind: EventSignal, Step: step, Signal: &signals[i]})
			}
			if r.cfg.SignalPolicy != nil {
				if act := r.cfg.SignalPolicy(signals); act.AbortTurn {
					reason := act.Reason
					if reason == "" {
						reason = "child signalled abort"
					}
					return nil, r.failSpan(emit, turnSpan, fmt.Errorf("%w: %s", ErrSignalAbort, reason))
				}
			}
			// Inject the signals as a RoleSystem note so the parent model sees
			// them on the next step. Drained once from the sink, appended once —
			// no transient-stacking (unlike a re-derived snapshot).
			sigMsg := Message{Role: RoleSystem, Text: renderSignals(signals)}
			msgs = append(msgs, sigMsg)
			added = append(added, sigMsg)
		}
	}

	return nil, r.failSpan(emit, turnSpan, fmt.Errorf("%w (%d steps)", ErrMaxSteps, r.cfg.MaxSteps))
}

func (r *Runner) failSpan(emit func(Event), span core.Span, err error) error {
	span.RecordError(err)
	emit(Event{Kind: EventError, Error: err.Error()})
	return err
}

// consumeStream folds one model call, emitting deltas as they arrive.
// Thinking markers wrap contiguous reasoning: begin before the first
// reasoning delta, end when the step moves on to text, tool calls, or
// completes.
func consumeStream(stream Stream, step int, emit func(Event)) (*ProviderResponse, error) {
	var acc Accumulator
	thinking := false
	endThinking := func() {
		if thinking {
			emit(Event{Kind: EventThinkingEnd, Step: step})
			thinking = false
		}
	}
	for {
		d, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		acc.Add(d)
		switch d.Kind {
		case DeltaReasoning:
			if !thinking {
				emit(Event{Kind: EventThinkingBegin, Step: step})
				thinking = true
			}
			emit(Event{Kind: EventThinkingDelta, Step: step, Text: d.Text})
		case DeltaText:
			endThinking()
			emit(Event{Kind: EventTextDelta, Step: step, Text: d.Text})
		case DeltaToolCallStart:
			endThinking()
		}
	}
	endThinking()
	return acc.Result(), nil
}

// structuredMaxAttempts bounds the finalizing Generate: one initial call plus
// retries when the model returns text that is not valid JSON. Two total keeps
// the extra cost low while absorbing the occasional near-miss.
const structuredMaxAttempts = 2

// finalizeStructured makes the schema-coercing Generate call over the finished
// conversation (msgs already includes the terminal assistant text) with no
// tools offered. It retries up to structuredMaxAttempts when the returned text
// is not valid JSON, folding each call's usage into usage. A provider error
// aborts (the caller asked for structured output and cannot get it); after the
// retry budget it returns the last output best-effort so a caller's Bind, not
// a lost turn, surfaces a still-malformed document.
func (r *Runner) finalizeStructured(ctx context.Context, instructions string, msgs []Message, usage *Usage, gen GenerationParams) (core.RawJSON, error) {
	var last string
	for attempt := 0; attempt < structuredMaxAttempts; attempt++ {
		finalReq := ProviderRequest{
			Instructions:   instructions,
			Messages:       msgs,
			ResponseSchema: r.cfg.ResponseSchema,
		}
		gen.applyTo(&finalReq)
		// The finalizing call offers no tools, so a ToolChoice carried from
		// the turn's params would ask the provider to force a call it cannot
		// make. Drop it here rather than in applyTo, which the step loop
		// shares.
		finalReq.ToolChoice = ToolChoice{}
		resp, err := r.cfg.Provider.Generate(ctx, finalReq)
		if err != nil {
			return core.RawJSON{}, err
		}
		if resp.Usage != nil {
			usage.InputTokens += resp.Usage.InputTokens
			usage.OutputTokens += resp.Usage.OutputTokens
			treeBudgetFrom(ctx).addTokens(resp.Usage.InputTokens + resp.Usage.OutputTokens)
		}
		last = resp.Text
		if json.Valid([]byte(last)) {
			return core.NewRawJSON(json.RawMessage(last)), nil
		}
	}
	return core.NewRawJSON(json.RawMessage(last)), nil
}

// callCancels tracks the in-flight tool calls of one turn so the
// control listener can cancel a specific call's context. Entries live
// from just before a call's tool-begin to just after it returns.
type callCancels struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (g *callCancels) add(id string, cancel context.CancelFunc) {
	g.mu.Lock()
	g.cancels[id] = cancel
	g.mu.Unlock()
}

func (g *callCancels) remove(id string) {
	g.mu.Lock()
	delete(g.cancels, id)
	g.mu.Unlock()
}

// cancel fires the named call's cancel func, or every in-flight one when
// id is empty. Unknown ids are a no-op.
func (g *callCancels) cancel(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if id == "" {
		for _, c := range g.cancels {
			c()
		}
		return
	}
	if c, ok := g.cancels[id]; ok {
		c()
	}
}

// shouldBreakOn decides whether a signal a child raised mid-flight breaks this
// (interruptible) dispatch's join barrier. Escalate always breaks (its
// must-handle contract); a custom FYI signal never does; a preempt breaks only
// when the parent grants it via PreemptGrant — the parent's authority over a
// claim the child cannot verify, so an untrusted child cannot unilaterally
// cancel its siblings. Installed as the sink's breakOn and called from a child's
// raise, so it must stay a pure read of config.
func (r *Runner) shouldBreakOn(sig Signal) bool {
	if !sig.Kind.interrupts() {
		return false
	}
	if sig.Kind == SignalPreempt {
		return r.cfg.PreemptGrant != nil && r.cfg.PreemptGrant(sig)
	}
	return true
}

// dispatch runs the step's tool calls concurrently, serializes event
// emission, and returns RoleTool messages in call order regardless of
// completion order, plus any upward Signals the calls' sub-agents raised.
// When reg is non-nil each call runs under its own child context registered by
// call ID, so a Control can cancel one call without touching its siblings or
// the turn. A fresh signal sink is installed per dispatch (not shared across
// the tree): a sub-agent spawned by one of these calls raises into THIS sink,
// its immediate parent's, and a nested dispatch shadows it for grandchildren.
//
// In an interruptible turn (RunnerConfig.Interruptible, issue 1167), the first
// barrier-breaking signal a child raises (shouldBreakOn: escalate always,
// preempt only when PreemptGrant honors it, custom never) cancels the remaining
// in-flight calls (they feed back "cancelled by user") and dispatch returns the
// partial results, so the step loop re-enters the model. parent stays the
// (sink-installed) step ctx and the calls run under a cancellable child of it,
// so a fan cancel reads as a per-call "cancelled by user" (parent live), never
// a turn abort.
func (r *Runner) dispatch(ctx context.Context, step int, calls []ToolCall, tools []core.ToolDef, emit func(Event), reg *callCancels) ([]Message, []Signal) {
	sink := &signalSink{}
	parent := withDispatchSink(ctx, sink)
	callBase := parent
	var cancelFan context.CancelFunc
	if r.cfg.Interruptible {
		sink.notify = make(chan struct{})
		sink.breakOn = r.shouldBreakOn
		callBase, cancelFan = context.WithCancel(parent)
		defer cancelFan()
	}
	results := make([]Message, len(calls))
	var emitMu sync.Mutex
	locked := func(ev Event) {
		emitMu.Lock()
		emit(ev)
		emitMu.Unlock()
	}

	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call ToolCall) {
			defer wg.Done()
			callCtx := callBase
			if reg != nil {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(callBase)
				reg.add(call.ID, cancel)
				defer func() {
					reg.remove(call.ID)
					cancel()
				}()
			}
			toolCtx, toolSpan := r.cfg.TracerProvider.StartSpan(callCtx, "agent.tool",
				core.Attribute{Key: "agent.tool.name", Value: call.Name})
			locked(Event{Kind: EventToolBegin, Step: step, ToolCall: &call})
			text := r.callTool(toolCtx, parent, step, call, tools, locked, toolSpan)
			toolSpan.End()
			results[i] = Message{Role: RoleTool, ToolCallID: call.ID, Text: text}
		}(i, call)
	}

	if cancelFan == nil {
		wg.Wait()
		return results, sink.drain()
	}
	// Interruptible: stop waiting the moment a child signals, cancel the rest,
	// then still wg.Wait for the cancelled calls to unwind so every result slot
	// is filled (providers require a result per tool call). No signal => the
	// select falls through on allDone, identical to the default barrier.
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	select {
	case <-allDone:
	case <-sink.notify:
		cancelFan()
		<-allDone
	}
	return results, sink.drain()
}

// toolReadOnly reports whether the named tool declares the readOnlyHint
// annotation in the step's offered set. It is the signal the read-only-auto
// approval tier keys on; an unknown tool or an absent hint is treated as
// not read-only (fail-safe: a tool that does not promise read-only gets the
// stricter path).
func toolReadOnly(tools []core.ToolDef, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			ro, _ := t.Annotations["readOnlyHint"].(bool)
			return ro
		}
	}
	return false
}

// callTool executes one call and renders the text fed back to the model.
// Every failure shape becomes model-visible text rather than a turn abort.
// ctx is the call's own (possibly Control-cancellable) context; parent is
// the step's. The two diverging — ctx cancelled while parent is live —
// identifies a per-call cancellation, which feeds back as "cancelled by
// user" so the model knows the user, not the tool, stopped the call.
func (r *Runner) callTool(ctx context.Context, parent context.Context, step int, call ToolCall, tools []core.ToolDef, emit func(Event), span core.Span) string {
	// cancelled identifies a per-call cancellation: this call's ctx is
	// done while the step's is live. Checked on every outcome shape —
	// a transport surfaces a cancelled call as an error, an in-process
	// source as an IsError result, and a tool racing the cancel may
	// even return success; the user's cancel wins over all three.
	// status is the terminal outcome recorded on the tool-call metric; the
	// closures and branches below set it as they resolve. Deferred so every
	// return path (including the early ones) is counted exactly once.
	start := time.Now()
	status := "ok"
	defer func() { r.metrics.toolDone(ctx, call.Name, status, time.Since(start)) }()

	cancelled := func() bool { return ctx.Err() != nil && parent.Err() == nil }
	cancelledText := func() string {
		status = "cancelled"
		span.SetAttribute("agent.tool.cancelled", "true")
		emit(Event{Kind: EventToolCancelled, Step: step, ToolCall: &call, Reason: "cancelled by user"})
		return "cancelled by user"
	}
	failed := func(err error) string {
		if cancelled() {
			return cancelledText()
		}
		status = "error"
		span.RecordError(err)
		emit(Event{Kind: EventToolError, Step: step, ToolCall: &call, Error: err.Error()})
		return fmt.Sprintf("tool call failed: %v", err)
	}

	if r.cfg.Tools == nil {
		return failed(fmt.Errorf("%w: %q (no tools offered)", ErrUnknownTool, call.Name))
	}

	if r.cfg.Approval != nil {
		dec, err := r.cfg.Approval.Approve(ctx, ApprovalRequest{
			ToolName: call.Name,
			Args:     call.Args,
			ReadOnly: toolReadOnly(tools, call.Name),
		})
		if err != nil {
			return failed(fmt.Errorf("agent: approval policy for %q: %w", call.Name, err))
		}
		if !dec.Allowed {
			reason := dec.Reason
			if reason == "" {
				reason = "denied by approval policy"
			}
			status = "denied"
			span.SetAttribute("agent.tool.denied", "true")
			emit(Event{Kind: EventToolDenied, Step: step, ToolCall: &call, Reason: reason})
			return "tool call not permitted: " + reason
		}
	}

	args := map[string]any{}
	if call.Args.Len() > 0 {
		if err := call.Args.Bind(&args); err != nil {
			return failed(fmt.Errorf("agent: tool %q arguments are not a JSON object: %w", call.Name, err))
		}
	}
	res, err := r.cfg.Tools.Call(ctx, call.Name, args)
	if err != nil {
		// A tool whose server is unreachable right now is a non-fatal miss, not
		// a failure: the model is told and the turn continues (mirrors denial /
		// cancellation). Reason (not Error) so error-keyed scorers don't count it.
		if errors.Is(err, ErrNotAvailableNow) && !cancelled() {
			status = "unavailable"
			span.SetAttribute("agent.tool.unavailable", "true")
			emit(Event{Kind: EventToolUnavailable, Step: step, ToolCall: &call, Reason: err.Error()})
			return err.Error()
		}
		return failed(err)
	}
	if cancelled() {
		return cancelledText()
	}
	span.SetAttribute("agent.tool.is_error", fmt.Sprint(res.IsError))
	emit(Event{Kind: EventToolEnd, Step: step, ToolCall: &call, ToolResult: res})
	text := toolResultText(res)
	if res.IsError {
		status = "tool_error"
		return "tool reported an error: " + text
	}
	return text
}

// toolResultText flattens a tool result for the model: text content items
// joined by newlines, falling back to marshaled structured content, then to
// a neutral placeholder so the model always receives something parseable.
func toolResultText(res *core.ToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) > 0 {
		out := parts[0]
		for _, p := range parts[1:] {
			out += "\n" + p
		}
		return out
	}
	if res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			return string(raw)
		}
	}
	return "(empty result)"
}
