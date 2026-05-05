# Sampling

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [reverse-call](./reverse-call.md)
> **Reachable from:** [reverse-call](./reverse-call.md) Next-to-read, [request-anatomy](./request-anatomy.md) Next-to-read
> **Spec:** [Sampling](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/sampling.go`, `core/sampling_test.go`

## Prerequisites

- You know how reverse-call origination works. → If not, read [reverse-call](./reverse-call.md).

## Context

Sampling lets the server ask the *client's* LLM for a completion. Useful when the server is reasoning at a meta level — analyzing code, summarizing content, generating prose — and needs a model invocation but doesn't want to embed its own. The host stays in control of model selection, cost, and approval.

## What this page will cover

- Wire shape: `sampling/createMessage` request with messages + model preferences, response with the completion
- Model preference fields: cost priority, speed priority, intelligence priority (hints, not commands)
- Context inclusion modes: `thisServer` / `allServers` / `none`
- The host-approval loop: client may surface the sampling request to the user before invoking the model
- Capability gating: client must declare `sampling` capability
- Why this exists at the protocol level (vs. just letting the server hold its own model): host control + cost attribution + uniform model access

## Next to read

*(Terminal — return to [reverse-call](./reverse-call.md) for the broader pattern.)*
