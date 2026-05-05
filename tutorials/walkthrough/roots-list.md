# Roots / list

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** leaf · **Prerequisites:** [reverse-call](./reverse-call.md)
> **Reachable from:** [reverse-call](./reverse-call.md) Next-to-read, [request-anatomy](./request-anatomy.md) Next-to-read
> **Spec:** [Roots](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/roots_allowed.go`, `core/roots_allowed_test.go`, `server/roots.go`, `server/roots_integration_test.go`

## Prerequisites

- You know how reverse-call origination works. → If not, read [reverse-call](./reverse-call.md).

## Context

Roots are filesystem locations (or other resource boundaries) that the client exposes to the server. A code-search tool needs to know which directories it can scan; a file-edit tool needs to know which paths are writable. The server asks the client for its roots via `roots/list` (a reverse call) and gets back a list. The host controls what's exposed.

## What this page will cover

- Wire shape: `roots/list` request from server, response with `[{ uri, name? }, ...]`
- Capability gating: client must declare `roots` capability
- The `roots.listChanged` capability: client signals "I'll notify when roots change" (`notifications/roots/list_changed` is the response)
- Security model: client decides what the server can see; the server's request is advisory
- URI shape: `file://` for filesystem paths, but the spec allows other schemes
- Examples: IDE host exposes the workspace dir; CLI host exposes whatever was passed on argv

## Next to read

*(Terminal — return to [reverse-call](./reverse-call.md) for the broader pattern.)*
