package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// DefaultMaxSteps bounds a turn when RunnerConfig.MaxSteps is zero. Eight
// model calls is generous for real workflows; hitting it usually means the
// model is looping on a failing tool.
const DefaultMaxSteps = 8

// ErrMaxSteps is returned (wrapped) by Run when the model keeps requesting
// tool calls past the step cap. Check with errors.Is.
var ErrMaxSteps = errors.New("agent: max steps exceeded")

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

	// MaxSteps caps model calls per turn. Zero means DefaultMaxSteps.
	MaxSteps int

	// TracerProvider opts the Runner into SEP 414 span emission:
	// agent.turn per Run, agent.step per model call, agent.tool per
	// dispatch, with ctx threading so client-side dispatch spans (and
	// through them server spans) stitch as children. Nil or
	// core.NoopTracerProvider means zero overhead, the repo-wide pattern.
	TracerProvider core.TracerProvider

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
	cfg RunnerConfig
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
	return &Runner{cfg: cfg}, nil
}

// TurnResult is the completed turn. Messages holds exactly the entries the
// turn appended (assistant messages and tool results, in order), so callers
// thread history as append(history, result.Messages...).
type TurnResult struct {
	Text         string    `json:"text,omitempty"`
	Messages     []Message `json:"messages"`
	Usage        Usage     `json:"usage"`
	Steps        int       `json:"steps"`
	FinishReason string    `json:"finishReason,omitempty"`
}

// Run executes one turn against history. Events stream to emit (nil is
// allowed); emit is never called concurrently. Tool failures of every kind
// (unknown tool, transport, bad args) are fed back to the model as
// error-marked tool results and the loop continues; only ctx cancellation,
// provider failure, or the step cap abort the turn. The returned error wraps
// ErrMaxSteps when the cap was hit.
func (r *Runner) Run(ctx context.Context, history []Message, emit func(Event)) (*TurnResult, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	ctx, turnSpan := r.cfg.TracerProvider.StartSpan(ctx, "agent.turn")
	defer turnSpan.End()
	emit(Event{Kind: EventTurnBegin})

	msgs := slices.Clone(history)
	var added []Message
	var usage Usage

	for step := 1; step <= r.cfg.MaxSteps; step++ {
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

		stream, err := r.cfg.Provider.Stream(stepCtx, ProviderRequest{
			Instructions: r.cfg.Instructions,
			Messages:     msgs,
			Tools:        tools,
		})
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
		}

		assistant := Message{Role: RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls}
		msgs = append(msgs, assistant)
		added = append(added, assistant)

		if len(resp.ToolCalls) == 0 {
			stepSpan.End()
			result := &TurnResult{
				Text:         resp.Text,
				Messages:     added,
				Usage:        usage,
				Steps:        step,
				FinishReason: resp.FinishReason,
			}
			turnSpan.SetAttribute("agent.steps", fmt.Sprint(step))
			turnSpan.SetAttribute("agent.finish_reason", resp.FinishReason)
			turnSpan.SetAttribute("agent.tokens.input", fmt.Sprint(usage.InputTokens))
			turnSpan.SetAttribute("agent.tokens.output", fmt.Sprint(usage.OutputTokens))
			emit(Event{Kind: EventTurnEnd, Result: result})
			return result, nil
		}

		toolMsgs := r.dispatch(stepCtx, step, resp.ToolCalls, tools, emit)
		stepSpan.End()
		if err := ctx.Err(); err != nil {
			return nil, r.failSpan(emit, turnSpan, err)
		}
		msgs = append(msgs, toolMsgs...)
		added = append(added, toolMsgs...)
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

// dispatch runs the step's tool calls concurrently, serializes event
// emission, and returns RoleTool messages in call order regardless of
// completion order.
func (r *Runner) dispatch(ctx context.Context, step int, calls []ToolCall, tools []core.ToolDef, emit func(Event)) []Message {
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
			toolCtx, toolSpan := r.cfg.TracerProvider.StartSpan(ctx, "agent.tool",
				core.Attribute{Key: "agent.tool.name", Value: call.Name})
			locked(Event{Kind: EventToolBegin, Step: step, ToolCall: &call})
			text := r.callTool(toolCtx, step, call, tools, locked, toolSpan)
			toolSpan.End()
			results[i] = Message{Role: RoleTool, ToolCallID: call.ID, Text: text}
		}(i, call)
	}
	wg.Wait()
	return results
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
func (r *Runner) callTool(ctx context.Context, step int, call ToolCall, tools []core.ToolDef, emit func(Event), span core.Span) string {
	failed := func(err error) string {
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
		return failed(err)
	}
	span.SetAttribute("agent.tool.is_error", fmt.Sprint(res.IsError))
	emit(Event{Kind: EventToolEnd, Step: step, ToolCall: &call, ToolResult: res})
	text := toolResultText(res)
	if res.IsError {
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
