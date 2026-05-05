# SSE resumption

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [transport-mechanics](./transport-mechanics.md)
> **Reachable from:** [transport-mechanics](./transport-mechanics.md) Next-to-read, [session-resumption](./session-resumption.md) Next-to-read
> **Spec:** [Streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `server/event_ids.go`, `server/event_store_test.go`, `server/sse_resumption_test.go`, `server/streamable_retry_test.go`

## Prerequisites

- You know the streamable HTTP wire format and how SSE event-ids and `Last-Event-ID` work at the spec level. → If not, read [transport-mechanics](./transport-mechanics.md).

## Context

[Transport-mechanics](./transport-mechanics.md) covered the surface: the standing GET stream may drop, the client reconnects with `Last-Event-ID`, and the server replays from there. This page goes into the implementation — what the server has to remember to make replay possible, how the event store is structured, and what happens to in-flight responses when an SSE stream drops mid-call.

## What this page will cover

- The event store: per-stream ring of (event-id, payload) tuples
- Retention policy: how long does the server keep events for replay?
- The reconnect dance with `Last-Event-ID`: client sends, server replays from N+1
- In-flight response recovery: a POST upgraded to SSE, stream dropped, response not yet delivered — what now?
- mcpkit's `event_ids.go` mechanics
- The interaction with `Mcp-Session-Id`: session-bound vs. stream-bound state
- When replay isn't enough: the experimental events extension goes beyond raw SSE event-id replay

## Next to read

- **[`experimental/ext/events/`](../../experimental/ext/events/README.md)** *(branch, target-shape)* — mcpkit's exploration of events as a first-class concept beyond raw SSE event-id replay.
