# Middleware composition

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** branch *(of [request-anatomy](./request-anatomy.md))* · **Prerequisites:** [request-anatomy](./request-anatomy.md)
> **Reachable from:** [request-anatomy](./request-anatomy.md) Next-to-read
> **Spec:** *(implementation; no normative spec)* · **Code:** `server/middleware.go`, `server/sending_middleware_test.go`, `client/middleware.go`, `server/middleware_test.go`

## Prerequisites

- You know there are four conceptual middleware stacks and how each is named. → If not, read [request-anatomy](./request-anatomy.md).

## Context

[Per-request-anatomy](./request-anatomy.md) named the four stacks but didn't drill into how middleware is composed, ordered, or how it integrates with extensions like `ext/auth` and `ext/ui`. This page is the deep-dive on middleware mechanics — write order, run order, error propagation, how to write a middleware that knows about message kind, and how `ext/auth` / `ext/ui` plug in.

## What this page will cover

- The middleware shape: function that wraps the next handler in the chain
- Onion model: request flows in through registered order, response flows out in reverse
- Request-side middleware (`server.middleware`) vs. sending-side middleware (`server.sending_middleware`)
- `ext/auth`'s interception points: where token validation hooks in
- `ext/ui`'s interception points: server-list filtering, capability gating
- Error propagation: middleware that returns an error short-circuits the chain
- Middleware that conditionally inspects message kind (request vs. response vs. notification)
- Concurrency: middleware must be safe for concurrent invocation

## Next to read

- **[MRTR (SEP-2322)](./mrtr.md)** *(stub, root)* — Multi Round-Trip Requests: a tools/call extension where the server returns `IncompleteResult` with input requests + a signed token instead of holding the call open for synchronous reverse calls. Different mechanism, comparable end goal (server gets data from client mid-call) — worth understanding the contrast.
