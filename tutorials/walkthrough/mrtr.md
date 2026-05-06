# MRTR — Multi Round-Trip Requests (SEP-2322)

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** root · **Prerequisites:** [request-anatomy](./request-anatomy.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [extension-mechanisms](./extension-mechanisms.md) Next-to-read, [request-anatomy](./request-anatomy.md) Next-to-read
> **Branches into:** [tasks](./tasks.md) *(stub, root)*
> **Spec:** [SEP-2322 (MRTR)](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** [`server/mrtr.go`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) · [`client/mrtr.go`](https://github.com/panyam/mcpkit/blob/main/client/mrtr.go) · [`core/handler_context.go`](https://github.com/panyam/mcpkit/blob/main/core/handler_context.go) *(NewToolContextWithMRTR)* · [`server/mrtr_test.go`](https://github.com/panyam/mcpkit/blob/main/server/mrtr_test.go) · [`client/mrtr_test.go`](https://github.com/panyam/mcpkit/blob/main/client/mrtr_test.go)

## Prerequisites

- You understand how a `tools/call` dispatches and what the handler context provides. → If not, read [request-anatomy](./request-anatomy.md).
- You know what an ephemeral capability is and the `_meta`-extension pattern. → If not, read [extension-mechanisms](./extension-mechanisms.md).

## Context

MRTR (**Multi Round-Trip Requests**, SEP-2322) lets a `tools/call` pause for input *without holding the call open*. Instead of awaiting a synchronous reverse call (sampling, elicitation, roots/list) inside the handler, the server returns `IncompleteResult` with a list of input requests + a signed `requestState` token. The client resolves the inputs, retries the same `tools/call` with `inputResponses` + the echoed token, and the server completes (or asks for another round). **The server keeps no state between rounds — the token is the round handle.**

mcpkit's default client-side `InputHandler` bridges MRTR's input requests onto the same handlers reverse calls use (sampling, elicitation, roots) — so a host that already supports those gets MRTR for free.

## What this page will cover

- Wire shape: `IncompleteResult`, the `inputRequests` map, `requestState`, the round retry with `inputResponses`
- Server-side: returning `IncompleteResult` from a handler, what's in the signed token, why HMAC + TTL, why stateless across rounds matters
- Client-side: `CallToolWithInputs`, `InputHandler` shape, `DefaultInputHandler`'s bridge to existing capability handlers, `WithMaxMRTRRounds` bounding, `ErrMRTRMaxRounds`
- MRTR vs reverse calls — comparison table: detach-friendliness, server-side state, latency, retry semantics, when to pick which
- Composition with tasks (v2): a task that returns `IncompleteResult`; how the task store and MRTR token interact; the deferred conformance scenario in `make testconf-mrtr`

## Next to read

- **[Tasks](./tasks.md)** *(stub, root)* — long-running operations; tasks v2 returns `IncompleteResult`, the same shape MRTR uses for `tools/call`.
- **[Reverse-call mechanics](./reverse-call.md)** *(stub, root)* — the synchronous-during-handler alternative; understanding the contrast clarifies when to use which.
