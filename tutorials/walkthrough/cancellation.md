# Cancellation deep-dive

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [notifications](./notifications.md)
> **Reachable from:** [notifications](./notifications.md) Next-to-read, [tasks](./tasks.md) Next-to-read
> **Spec:** [Cancellation utility](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/jsonrpc.go`, `server/dispatch.go`, mcpkit's `ctx.Done()` propagation paths

## Prerequisites

- You know the basic shape of `notifications/cancelled` and that cancellation is best-effort. → If not, read [notifications](./notifications.md).

## Context

[Notifications](./notifications.md) covered the basic mechanics: `notifications/cancelled` carries a `requestId`, the receiver cancels best-effort, the `initialize` request is the one prohibition. This page covers the messy details — race conditions when responses cross cancellations in flight, partial-state handling, the timeout-vs-cancel distinction, and how mcpkit's `ctx.Done()` propagates cancellation through Go goroutines and into outstanding reverse calls.

## What this page will cover

- The race scenarios:
  - Cancel arrives before handler starts
  - Cancel arrives during handler execution
  - Cancel arrives after response is on the wire (response in flight)
  - Cancel arrives after response delivered
- Partial-state handling: handler that has emitted some progress notifications + has reverse calls in flight
- Timeout vs. cancel: mcpkit semantics, who initiates each, observable difference
- `ctx.Done()` propagation in the handler
- Cancel-of-reverse-call: when forward is cancelled, the back-pointer fires cancel on outstanding reverses (the `pending[reverseId] → originated-by → forwardId` chain from [reverse-call mechanics](./reverse-call.md))
- The asymmetry: `notifications/cancelled` is fire-and-forget, but the cancellation effect is observable (the response stops coming)

## Next to read

*(Terminal — return to [notifications](./notifications.md) for the broader notifications model, or [tasks](./tasks.md) for cancellation in long-running operations.)*
