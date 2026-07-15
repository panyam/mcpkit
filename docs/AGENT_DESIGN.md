# agent/ Design

The `agent/` sub-module is mcpkit's host layer: the piece that sits above `core/`, `server/`, and `client/` and runs the agentic loop. An application embeds it to become an MCP host: it connects MCP servers, hands their tools to a model, dispatches the model's tool calls, routes elicitation to a user interface, and streams the whole turn as typed events.

This document is the contract the module's tickets build against (tracking epic: mcpkit issue 895). It pins the decisions that later milestones inherit; implementation details live with their tickets.

## Scope

What the module is:

- An LLM provider abstraction with streaming and tool-call support
- The multi-step tool loop (the Runner)
- Tool aggregation across MCP servers and host-local functions (ToolSource)
- Elicitation routing to a pluggable UI callback
- Policy seams for context injection and event-initiated turns

What the module is not (deliberately):

- Not a chat service: sessions, wire transports (WebSocket/SSE), reconnect cursors, and persistence live in applications built on the module. See Surfaces below for the promotion path.
- Not a memory or multi-agent framework. No episodic memory, no supervision trees, no A2A. If a need lands, it gets its own design pass.
- Not a protocol extension. Nothing here changes what travels between host and server beyond spec-legal vendor `_meta` keys.

Dependency rule: `agent/` depends on `core/` and `client/` (and may depend on other sub-modules such as `ext/ui`). Nothing in the root module or other sub-modules may depend on `agent/`, and LLM-provider dependencies exist only here. `agent/CONSTRAINTS.md` makes this checkable.

## The four seams

### Provider

```go
// Sketch, finalized in the provider ticket (issue 884).
type Provider interface {
    // Stream runs one model call. Deltas arrive on the returned stream:
    // text, reasoning, tool-call start/args/end, finish, usage.
    Stream(ctx context.Context, req Request) (Stream, error)
    // Generate is the non-streaming call used for utility work
    // (structured output, naming, summaries).
    Generate(ctx context.Context, req Request) (Response, error)
}
```

One production implementation ships first: OpenAI-compatible chat completions, which covers lmstudio, vllm, LiteLLM-style proxies, and gateways. A `StubProvider` plays back scripted turns for deterministic tests. Failover wraps Provider (a Provider that fronts a main and a backup) so the Runner never knows about it.

### Runner

The loop: given (instructions, Provider, ToolSource, history), call the model, dispatch tool calls (parallel where independent), append results, repeat until final text, bounded by a step cap and ctx cancellation. Tool handler errors become tool-result errors fed back to the model, not loop aborts.

The Runner emits typed events (see Surfaces): turn-begin, thinking-begin/delta/end, text-delta, tool-begin/end/error, elicitation-request, turn-end, error.

### ToolSource

```go
// Sketch, finalized in the tool-source ticket (issue 886).
type ToolSource interface {
    Tools(ctx context.Context) ([]ToolDef, error)
    Call(ctx context.Context, name string, args json.RawMessage) (core.ToolResponse, error)
}
```

Adapters: `ClientSource` (a single `client.Client`, MRTR-aware calls with a pluggable InputHandler), `FuncSource` (host-local typed functions via `core.GenerateSchema`), and `MultiSource` (aggregation). **Decision (issue 886): lift the registry pattern, do not depend on `ext/ui`.** ServerRegistry's value is client lifecycle plus apps-bridge management; ToolSource only needs the index shape, and aggregating ToolSources is strictly more general (it composes Func, Client, and any future registry adapter). Collision semantics mirror the registry: all claimants kept, a resolver callback for ambiguous bare-name calls, and the model-facing list exposes collisions only in qualified `sourceID_name` form so every tool stays reachable without duplicate names. A thin `RegistrySource` adapter over `ext/ui` lands with the apps integration, where the dependency belongs.

### Policy hooks

Two seams, both no-op by default:

- **Injection policy**: consumes events from connected servers (experimental events extension), decides what enters model context and how: priority under a context budget, aggregation windows, template rendering, sensitivity gating (issue 893).
- **Trigger policy**: declarative bindings (event, filter, instruction template) that start a Runner turn without a user message, mediated by rate-limit slots, consent, and budget caps (issue 894).

## Surfaces: how UIs consume the module

Three interface families build on the module: CLIs, web frontends, and desktop/native apps. They reduce to two consumption modes, and the module defines one canonical contract that serves both:

| Surface | Mode | What it uses |
|---|---|---|
| CLI (`cmd/agentchat`) | in-process | Import the module, subscribe to the event stream, implement the elicitation callback in-terminal |
| Web | wire | A host application embeds the module and maps the event stream 1:1 onto WebSocket or SSE; user input, cancel, and elicitation responses come back as requests |
| Desktop/native | either | Go-native shells (Wails, Fyne) consume in-process like the CLI; webview shells consume the wire like web |

The canonical contract is the in-process one: the Runner input API (submit turn, cancel, history access), the typed event stream, and the elicitation callback. The wire is a projection of it, never a second design.

Two rules keep that projection one-to-one:

1. **Every Runner event type is wire-serializable from day one.** JSON tags, a stable `kind` discriminator, no Go-only payloads (channels, funcs, interfaces without concrete marshaling) in event fields. Enforced by a round-trip test (see `agent/CONSTRAINTS.md`).
2. **The wire layer stays out of the module until it has two consumers.** The first web host builds the WebSocket mapping in its own package; when a second application needs it, the mapping is promoted into `agent/` (or a sibling package) as-is. This mirrors how the fire-and-forget-then-subscribe chat shape should work: submit returns an id immediately, the event stream carries the turn.

## Vendor `_meta` prefix

All vendor-namespaced `_meta` keys emitted or consumed by this module use the prefix:

```
io.github.panyam.mcpkit/
```

Rationale: MCP reserves `io.modelcontextprotocol/` for spec-defined fields, and vendor prefixes should be reverse-DNS of something the vendor controls; `panyam.github.io` qualifies. First planned keys: `io.github.panyam.mcpkit/context-hint` (injection hints on event definitions) and `io.github.panyam.mcpkit/trigger` (trigger bindings). If a shorter owned domain ever materializes, the rename is a one-time migration with a reading shim, acceptable while the keys are pre-standardization.

## Testing strategy

The `StubProvider` is the backbone: scripted turns (text, tool-call sequences, errors) make the Runner fully deterministic in CI with no network and no model. Loop behaviors (multi-step, parallel dispatch, cancellation, step caps, error recovery) are unit-tested against it. Integration tests drive agentchat against in-repo example servers (skills, tasks-v2, auth) behind a build tag or make target, mirroring how conformance suites run today. The module's sanity test additionally guards the sub-module wiring itself (replace directive, go.sum drift), which is the documented failure mode for mcpkit sub-modules.
