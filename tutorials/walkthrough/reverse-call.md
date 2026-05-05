# Reverse-call mechanics

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** root · **Prerequisites:** [bring-up](./bringup.md), [transport-mechanics](./transport-mechanics.md), [request-anatomy](./request-anatomy.md)
> **Reachable from:** [transport-mechanics](./transport-mechanics.md) Next-to-read, [request-anatomy](./request-anatomy.md) Next-to-read, [extension-mechanisms](./extension-mechanisms.md) Next-to-read
> **Branches into:** [elicitation](./elicitation.md), [sampling](./sampling.md), [roots-list](./roots-list.md)
> **Spec:** [Client features (sampling, elicitation, roots)](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/handler_context.go`, `core/sampling.go`, `core/elicitation.go`, `core/roots_allowed.go`, `server/mrtr.go`, `client/mrtr.go`

## Prerequisites

- You know how the per-request anatomy dispatches and how handler context is built. → If not, read [request-anatomy](./request-anatomy.md).
- You know the wire-level pending-id table and that reverse-call origination is gated by handler context (not by the wire). → If not, read [transport-mechanics](./transport-mechanics.md).

## Context

When a server handler needs information from the client mid-call — sample an LLM completion, ask the user for input, list the host's filesystem roots — it originates a *reverse call*. This page walks one such call (`tools/call → elicitation/create`) end-to-end with code references, and pins down the parent-id back-pointer that propagates cancellation through reverse calls.

## What this page will cover

- Worked example: `tools/call` whose handler invokes `elicitation/create`, with wire bytes for both directions
- Wire-level: server allocates id from its own space; no parent field on the wire
- Handler-level: the request hook on the handler context, the `pending[reverseId] → originated-by → forwardId` back-pointer
- Cancellation propagation: when the forward call is cancelled, the back-pointer drives cleanup of in-flight reverse calls
- mrtr-on-both-sides symmetry: client-side dispatches the reverse call to a host-provided delegate (sampling delegate, elicitation UI handler, etc.)
- The three concrete call types as applications: sampling, elicitation, roots/list

## Next to read

- **[Elicitation](./elicitation.md)** *(stub leaf)* — Form mode vs. URL mode, security implications.
- **[Sampling](./sampling.md)** *(stub leaf)* — model selection hints, host-approval loop.
- **[Roots/list](./roots-list.md)** *(stub leaf)* — client→server reverse for filesystem roots.
- **[Tasks](./tasks.md)** *(stub root)* — uses reverse calls for long-running operations needing user interaction.
