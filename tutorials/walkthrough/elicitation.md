# Elicitation

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [reverse-call](./reverse-call.md)
> **Reachable from:** [reverse-call](./reverse-call.md) Next-to-read, [request-anatomy](./request-anatomy.md) Next-to-read
> **Spec:** [Elicitation](https://modelcontextprotocol.io/specification/draft/client/elicitation) · **Code:** `core/elicitation.go`, `core/elicitation_test.go`, `examples/elicitation/`

## Prerequisites

- You know how reverse-call origination works (handler context, parent-id back-pointer). → If not, read [reverse-call](./reverse-call.md).

## Context

Elicitation is the server's way to ask the user for input mid-tool-call. Server emits `elicitation/create` as a reverse call; client surfaces the request to the user; user responds; server's handler resumes with the input. Two modes — Form (structured input via JSON Schema) and URL (host opens a URL outside the MCP client for sensitive data).

## What this page will cover

- **Form mode**: server provides a JSON Schema, client renders a form, user submits structured data
- **URL mode**: server provides a URL, host opens it in its UI; data captured outside MCP (for OAuth-style flows where credentials shouldn't pass through the MCP client)
- Security model: what the user sees, when they consent, what the server learns
- Wire shape: `elicitation/create` request, `elicitation/create` response with the user's input
- Capability gating: client must declare `elicitation` capability
- Cancellation: user cancels mid-form, propagation back to handler

## Next to read

*(Terminal — return to [reverse-call](./reverse-call.md) for the broader pattern.)*
