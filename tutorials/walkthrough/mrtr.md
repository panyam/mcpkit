# MRTR (Message Routing Through Middleware, SEP-2322)

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** branch *(of [request-anatomy](./request-anatomy.md))* · **Prerequisites:** [request-anatomy](./request-anatomy.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [request-anatomy](./request-anatomy.md) Next-to-read, [extension-mechanisms](./extension-mechanisms.md) Next-to-read
> **Spec:** [SEP-2322 (MRTR)](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `server/mrtr.go`, `client/mrtr.go`, `server/mrtr_test.go`, `client/mrtr_test.go`

## Prerequisites

- You know the four conceptual middleware stacks (client × {send, recv}, server × {send, recv}). → If not, read [request-anatomy](./request-anatomy.md).
- You know how SEP-tracked extensions land in mcpkit. → If not, read [extension-mechanisms](./extension-mechanisms.md).

## Context

[Per-request-anatomy](./request-anatomy.md) describes four conceptual middleware stacks. MRTR is the unification — a single routing layer that takes (direction, side) as parameters and runs middleware uniformly for all four. Same code path for forward calls and reverse calls, both sides of the wire. SEP-2322 codifies the API.

## What this page will cover

- The per-direction-per-side abstraction collapsed into one routing pipeline
- How the same MRTR runs middleware for: forward request handling, response sending, reverse-call origination, reverse-call response handling
- The ephemeral capability flag: mcpkit signals MRTR support but doesn't require it
- The 7-scenario conformance suite (`make testconf-mrtr`) and the 1 deferred task-composition scenario
- Wiring: how to plug middleware into MRTR vs. legacy middleware registries
- Migration: which middleware should move to MRTR, which can stay legacy

## Next to read

- **[Middleware composition](./middleware.md)** *(stub branch)* — request-side vs. sending-side in detail; legacy middleware vs. MRTR.
