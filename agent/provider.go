package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Role identifies who authored a Message.
type Role string

// Message roles. RoleTool carries a tool result back to the model and must
// set ToolCallID; RoleAssistant messages may carry ToolCalls.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	// RoleSystem carries injected context (events, trigger instructions)
	// into the conversation; providers map it to their native system
	// slot. Distinct from ProviderRequest.Instructions, which is the
	// static prompt: RoleSystem messages live in history and thread
	// across turns like any other message.
	RoleSystem Role = "system"
)

// Message is one conversation entry in provider-neutral form. All fields are
// JSON-tagged so histories can cross a wire unchanged (constraint A2).
type Message struct {
	Role Role `json:"role"`

	// Text is the message content. Empty is legal for assistant messages
	// that only carry tool calls.
	Text string `json:"text,omitempty"`

	// ToolCalls holds the calls an assistant message requested.
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`

	// ToolCallID links a RoleTool message to the assistant ToolCall it
	// answers.
	ToolCallID string `json:"toolCallId,omitempty"`

	// Timestamp records when the message was said. Zero means unstamped;
	// RunStore implementations stamp zero-Timestamp messages with their
	// own clock on AppendMessages, and a caller-set non-zero value wins
	// (a surface can stamp the user message at keypress). Providers
	// never map this field into request bodies — it is session metadata,
	// not model input.
	Timestamp time.Time `json:"timestamp,omitzero"`
}

// ToolCall is one model-requested tool invocation.
type ToolCall struct {
	// ID is the provider-assigned call identifier, echoed on the RoleTool
	// result message.
	ID string `json:"id"`
	// Name is the tool name as listed to the model.
	Name string `json:"name"`
	// Args is the JSON arguments object. core.RawJSON so readers share
	// one parse (Bind for typed decode, Raw for display); wire shape is
	// identical to a raw message.
	Args core.RawJSON `json:"args"`
}

// ProviderRequest is one model call in provider-neutral form.
type ProviderRequest struct {
	// Instructions is the system prompt. Providers map it to their native
	// system slot; it is not a Message so histories stay role-clean.
	Instructions string `json:"instructions,omitempty"`

	// Messages is the conversation so far, oldest first.
	Messages []Message `json:"messages"`

	// Tools lists what the model may call. Nil means no tools offered.
	Tools []core.ToolDef `json:"tools,omitempty"`

	// Temperature overrides the provider default when non-nil. Set it on a
	// turn through RunnerConfig.Generation or TurnRequest.Generation.
	//
	// Not universally supported, and the unsupported case is a hard failure
	// rather than a graceful one: current Anthropic models reject sampling
	// parameters outright, so a non-nil Temperature makes the request 400
	// instead of being ignored. Leave it nil for those models. Providers
	// forward this field as given and do not screen it by model — mcpkit
	// keeps no per-model capability table — so the vendor's own error is
	// the diagnostic, carried verbatim in ProviderError.Body.
	Temperature *float64 `json:"temperature,omitempty"`

	// MaxTokens caps the completion length when positive. Set it on a turn
	// through RunnerConfig.Generation or TurnRequest.Generation.
	//
	// AnthropicProvider always sends a max_tokens (the Messages API requires
	// one), defaulting to AnthropicConfig.MaxTokens when this is zero.
	MaxTokens int `json:"maxTokens,omitempty"`

	// ToolChoice biases tool calling for this request. Empty is the
	// provider default ("auto"). Use ToolChoiceRequired to force some
	// tool call, ToolChoiceNone to forbid, or ToolChoiceFunc(name) to
	// force a specific tool. Support varies across OpenAI-compatible
	// servers; a server that ignores it degrades to "auto".
	//
	// Pairs with RunnerConfig.Selector (narrow the offered set) to steer a
	// proactive or injected turn toward acting rather than only replying:
	// set Selector on the config and ToolChoice on that turn's
	// TurnRequest.Generation.
	ToolChoice ToolChoice `json:"toolChoice,omitempty"`

	// ResponseSchema, when set, asks Generate for structured output
	// conforming to this JSON Schema. Ignored by Stream.
	ResponseSchema core.RawJSON `json:"responseSchema,omitempty"`
}

// GenerationParams are the model-facing generation knobs a caller sets for a
// turn: the subset of ProviderRequest that says *how* to generate rather than
// *what* to generate from. The Runner copies them onto every ProviderRequest
// it builds, so they reach both the streaming step loop and the finalizing
// Generate of a structured-output turn.
//
// Set defaults for every turn on RunnerConfig.Generation and override them for
// one turn on TurnRequest.Generation. Merge is per field, and a zero field
// inherits: a TurnRequest that sets only ToolChoice keeps the config's
// Temperature and MaxTokens. The corollary is that a turn cannot un-set a
// config default back to the provider's own default — set the field to the
// value you want instead.
//
// ResponseSchema is deliberately not here. It lives on RunnerConfig because it
// selects a different turn shape (a finalizing Generate call) rather than
// tuning the calls a turn already makes.
//
// Support varies by provider and a rejected parameter is a failed request, not
// a silently ignored one. See ProviderRequest.Temperature.
type GenerationParams struct {
	// Temperature overrides the provider default when non-nil.
	//
	// Setting it is not universally safe. Current Anthropic models reject
	// sampling parameters outright, so a non-nil Temperature makes the
	// request fail with a 400 rather than being ignored, and mcpkit keeps no
	// per-model capability table that would catch this first. Leave it nil
	// unless the target model is known to accept it. Full contract on
	// ProviderRequest.Temperature.
	Temperature *float64 `json:"temperature,omitempty"`

	// MaxTokens caps the completion length when positive. Zero leaves the
	// decision to the provider, except on AnthropicProvider, whose API
	// requires a cap and which substitutes AnthropicConfig.MaxTokens.
	//
	// A cap that truncates mid-turn ends the call with a length-flavored
	// FinishReason and no error: the Runner treats the short completion as
	// the model's answer, so too small a value silently degrades a turn
	// rather than failing it.
	MaxTokens int `json:"maxTokens,omitempty"`

	// ToolChoice biases tool calling. The zero value is the provider
	// default ("auto"). Support varies across OpenAI-compatible servers and
	// one that ignores it degrades to "auto", so forcing a call is a
	// request, not a guarantee. Full contract on ProviderRequest.ToolChoice.
	ToolChoice ToolChoice `json:"toolChoice,omitempty"`
}

// merge returns p overlaid with over: each field of over that is set wins,
// each field that is zero inherits from p. Used to resolve
// TurnRequest.Generation against RunnerConfig.Generation.
func (p GenerationParams) merge(over GenerationParams) GenerationParams {
	out := p
	if over.Temperature != nil {
		out.Temperature = over.Temperature
	}
	if over.MaxTokens != 0 {
		out.MaxTokens = over.MaxTokens
	}
	if !over.ToolChoice.IsZero() {
		out.ToolChoice = over.ToolChoice
	}
	return out
}

// applyTo copies the params onto req. Kept as one place so the streaming and
// finalizing request builders cannot drift.
func (p GenerationParams) applyTo(req *ProviderRequest) {
	req.Temperature = p.Temperature
	req.MaxTokens = p.MaxTokens
	req.ToolChoice = p.ToolChoice
}

// ToolChoice is the request-level tool-calling bias. The zero value ("")
// means the provider default (auto). It marshals to the OpenAI-compatible
// wire form: the bare strings "auto"/"required"/"none", or the
// {type:function, function:{name}} object for a forced tool.
type ToolChoice struct {
	// Mode is "", "auto", "required", "none", or "function".
	Mode string `json:"mode,omitempty"`
	// Name is the forced tool for Mode == "function".
	Name string `json:"name,omitempty"`
}

// ToolChoiceAuto lets the model decide (the default; same as the zero value).
var ToolChoiceAuto = ToolChoice{Mode: "auto"}

// ToolChoiceRequired forces the model to call some tool.
var ToolChoiceRequired = ToolChoice{Mode: "required"}

// ToolChoiceNone forbids tool calls for this request.
var ToolChoiceNone = ToolChoice{Mode: "none"}

// ToolChoiceFunc forces the model to call the named tool.
func ToolChoiceFunc(name string) ToolChoice { return ToolChoice{Mode: "function", Name: name} }

// IsZero reports whether no choice was set (provider default applies).
func (tc ToolChoice) IsZero() bool { return tc.Mode == "" }

// wire renders the OpenAI-compatible tool_choice value, or nil when unset.
func (tc ToolChoice) wire() any {
	switch tc.Mode {
	case "", "auto":
		if tc.Mode == "" {
			return nil
		}
		return "auto"
	case "required", "none":
		return tc.Mode
	case "function":
		return map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
	default:
		return nil
	}
}

// Usage reports token consumption for one model call. TurnResult.Usage is the
// sum across a turn; see its doc for what that total does and does not cover.
//
// Both counts are as the provider reported them, not as mcpkit measured them.
// A provider that reports no usage yields a nil *Usage rather than a zero one,
// so the caller can tell "not reported" from "reported as zero". The Runner
// treats a missing report as zero when summing, which means a provider that
// under-reports produces an undercount rather than an error.
type Usage struct {
	// InputTokens is the prompt side of the call: instructions, history, and
	// tool definitions as the provider counted them. Cache reads and writes
	// are not broken out; whatever the provider folds into its own input
	// count is what appears here.
	InputTokens int `json:"inputTokens"`

	// OutputTokens is the completion side, including tokens spent on tool
	// calls and on reasoning the provider billed for, even when that
	// reasoning never reached Delta.Text.
	OutputTokens int `json:"outputTokens"`
}

// DeltaKind discriminates Delta payloads.
type DeltaKind string

// Delta kinds. Tool calls stream as one DeltaToolCallStart (carrying ID and
// Name) followed by any number of DeltaToolCallArgs fragments for the same
// Index; there is no explicit end marker, the next start or the finish delta
// closes the call (fold with Accumulator).
const (
	DeltaText          DeltaKind = "text"
	DeltaReasoning     DeltaKind = "reasoning"
	DeltaToolCallStart DeltaKind = "tool-call-start"
	DeltaToolCallArgs  DeltaKind = "tool-call-args"
	DeltaFinish        DeltaKind = "finish"
	DeltaUsage         DeltaKind = "usage"
)

// Delta is one streamed increment of a model response. Wire-serializable by
// design (constraint A2): surfaces may forward deltas verbatim.
type Delta struct {
	Kind DeltaKind `json:"kind"`

	// Text carries the fragment for DeltaText, DeltaReasoning, and
	// DeltaToolCallArgs.
	Text string `json:"text,omitempty"`

	// Index is the tool-call slot for DeltaToolCallStart and
	// DeltaToolCallArgs; parallel calls interleave on distinct indexes.
	Index int `json:"index,omitempty"`

	// ToolCallID and ToolName are set on DeltaToolCallStart.
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`

	// FinishReason is set on DeltaFinish ("stop", "tool_calls", ...,
	// provider vocabulary passed through).
	FinishReason string `json:"finishReason,omitempty"`

	// Usage is set on DeltaUsage.
	Usage *Usage `json:"usage,omitempty"`
}

// Stream delivers Deltas for one model call. Recv returns io.EOF after the
// final delta. Close releases the underlying connection and is safe to call
// at any point, including concurrently with Recv (which then returns an
// error). Streams are single-consumer.
type Stream interface {
	Recv() (Delta, error)
	Close() error
}

// ProviderResponse is a completed model call: the fold of a delta stream, or
// the direct result of Generate. One response is one call, never a whole turn;
// the Runner makes as many of these as the step loop needs.
type ProviderResponse struct {
	// Text is the assistant's message content. Empty is normal and not an
	// error: a call that only requested tools produces no text.
	Text string `json:"text,omitempty"`

	// Reasoning is provider-exposed reasoning text, folded from
	// DeltaReasoning. Empty for providers that expose none, which is most of
	// them. This is plain text for display and never carries provider-opaque
	// state such as signed thinking blocks, which constraint A9 keeps off the
	// neutral types entirely.
	Reasoning string `json:"reasoning,omitempty"`

	// ToolCalls holds the calls the model requested, in the order the
	// provider emitted them. Parallel calls all appear here; the Runner
	// dispatches them and feeds one RoleTool message back per call.
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`

	// FinishReason is the provider's own vocabulary passed through
	// unmapped ("stop", "tool_calls", "length", and whatever else a given
	// provider emits). Compare against it only for a specific provider, and
	// branch on len(ToolCalls) rather than on this string when deciding
	// whether the loop should continue.
	FinishReason string `json:"finishReason,omitempty"`

	// Usage is what the provider reported for this call, or nil when it
	// reported nothing. Nil is distinct from a zero Usage.
	Usage *Usage `json:"usage,omitempty"`
}

// Provider is the LLM seam. Implementations must be safe for concurrent use;
// each Stream call is an independent model invocation.
type Provider interface {
	// Stream runs one model call and delivers the response incrementally.
	Stream(ctx context.Context, req ProviderRequest) (Stream, error)

	// Generate runs one model call to completion. When
	// req.ResponseSchema is set, implementations request structured
	// output conforming to it and return the JSON document in Text.
	Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error)
}

// Accumulator folds a delta stream into a ProviderResponse. Zero value is
// ready to use. Not safe for concurrent use.
type Accumulator struct {
	resp    ProviderResponse
	text    strings.Builder
	reason  strings.Builder
	argBufs map[int]*strings.Builder
	calls   map[int]*ToolCall
	order   []int
}

// Add folds one delta.
func (a *Accumulator) Add(d Delta) {
	switch d.Kind {
	case DeltaText:
		a.text.WriteString(d.Text)
	case DeltaReasoning:
		a.reason.WriteString(d.Text)
	case DeltaToolCallStart:
		if a.calls == nil {
			a.calls = make(map[int]*ToolCall)
			a.argBufs = make(map[int]*strings.Builder)
		}
		a.calls[d.Index] = &ToolCall{ID: d.ToolCallID, Name: d.ToolName}
		a.argBufs[d.Index] = &strings.Builder{}
		a.order = append(a.order, d.Index)
		if d.Text != "" {
			a.argBufs[d.Index].WriteString(d.Text)
		}
	case DeltaToolCallArgs:
		if buf, ok := a.argBufs[d.Index]; ok {
			buf.WriteString(d.Text)
		}
	case DeltaFinish:
		a.resp.FinishReason = d.FinishReason
	case DeltaUsage:
		a.resp.Usage = d.Usage
	}
}

// Result returns the folded response. Tool-call argument fragments are
// joined in stream order; an empty argument buffer becomes the empty JSON
// object so callers can always unmarshal Args.
func (a *Accumulator) Result() *ProviderResponse {
	out := a.resp
	out.Text = a.text.String()
	out.Reasoning = a.reason.String()
	for _, idx := range a.order {
		call := *a.calls[idx]
		args := a.argBufs[idx].String()
		if args == "" {
			args = "{}"
		}
		call.Args = core.NewRawJSON(json.RawMessage(args))
		out.ToolCalls = append(out.ToolCalls, call)
	}
	return &out
}
