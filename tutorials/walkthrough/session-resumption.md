# Re-init / session resumption

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [bring-up](./bringup.md)
> **Reachable from:** [bring-up](./bringup.md) Next-to-read
> **Spec:** [Streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `client/client_reconnect.go`, `client/client_reconnect_test.go`, `server/session_timeout_test.go`

## Prerequisites

- You know the bring-up phases and what's established at the end. → If not, read [bring-up](./bringup.md).

## Context

What happens when the underlying transport drops while a session is live? For stdio, the process probably died — there's no resumption. For streamable HTTP, the session id is the continuity mechanism: the client may reconnect (open a new TCP connection, a new GET) using the same `Mcp-Session-Id`, and the server picks up where it left off. This page covers what's preserved across reconnect, what isn't, and how this differs from full re-init.

## What this page will cover

- Session-bound vs. transport-bound state: what survives a TCP drop
- The reconnect path on streamable HTTP: new GET with `Mcp-Session-Id` + `Last-Event-ID`
- Server-side session timeout: how long does the server hold a session open without traffic?
- Client-side reconnect logic: backoff, max retries, give up
- When to re-init vs. when to resume: the server may invalidate session id (signaled by 404 or specific status), forcing the client to start over
- Interaction with in-flight calls: pending requests on either side after reconnect — do they replay, retry, or fail?

## Next to read

- **[SSE resumption](./sse-resumption.md)** *(stub leaf)* — drills into the per-stream `Last-Event-ID` replay mechanic that supports session resumption.
