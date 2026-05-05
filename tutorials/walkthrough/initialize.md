# Initialize deep-dive

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [bring-up](./bringup.md)
> **Reachable from:** [bring-up](./bringup.md) Next-to-read
> **Spec:** [Lifecycle / capabilities](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/protocol.go`

## Prerequisites

- You've seen the basic `initialize` handshake from bring-up. → If not, read [bring-up](./bringup.md).

## Context

[Bring-up](./bringup.md) sketched the `initialize` handshake at high level. This page is the reference: every capability flag the spec defines, what each gates, what canonical default values look like, and the version-negotiation edge cases (downgrade, refusal, undefined intermediate versions).

## What this page will cover

- Full capability flag enumeration:
  - Server: `tools`, `prompts`, `resources`, `logging`, plus per-area `listChanged` sub-flags; `experimental`
  - Client: `sampling`, `roots`, `elicitation`; `experimental`
- What each flag gates (which methods, which notifications)
- The version-negotiation algorithm: client offers, server picks ≤ what client offered, client accepts or hangs up
- Edge cases:
  - Server picks a version client didn't offer (spec violation — client must close)
  - Client offers no versions (`initialize` schema rejection)
  - Mid-session re-initialize attempt (forbidden)
- Server's `serverInfo` fields: name, version, instructions
- Client's `clientInfo` fields: name, version

## Next to read

*(Terminal — return to [bring-up](./bringup.md) for the lifecycle context.)*
