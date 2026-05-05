# Auth deep-dive

<!-- STUB -->

> [!IMPORTANT]
> **Stub page.** Header is filled out so the graph and links stay accurate, but the body below is an outline only. Track progress in [INDEX.md](./INDEX.md).

> **Kind:** root *(off-mainline)* · **Prerequisites:** [bring-up](./bringup.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [bring-up](./bringup.md) Next-to-read, [extension-mechanisms](./extension-mechanisms.md) Next-to-read
> **Spec:** [WWW-Authenticate](https://datatracker.ietf.org/doc/html/rfc6750), OAuth 2.x · **Code:** `core/auth.go`, `core/www_authenticate.go`, `core/authorization_denial_experimental.go`, `ext/auth/`

## Prerequisites

- You understand the bring-up phases, especially the pre-handshake auth phase for HTTP transports. → If not, read [bring-up](./bringup.md).
- You know what an extension is, particularly the "bring-up extension" style that extends connection establishment rather than the message exchange. → If not, read [extension-mechanisms](./extension-mechanisms.md).

## Context

Auth in MCP is a "bring-up extension." It doesn't use any of the four MCP message-level extension surfaces — no new methods, no new capabilities, no new notifications, no `_meta` fields. Instead it extends the HTTP layer below MCP: WWW-Authenticate / 401 / OAuth 2.x dance / bearer token on every subsequent request. mcpkit's `ext/auth/` package implements OAuth client + server, JWT validation, Protected Resource Metadata (PRM), and fine-grained-auth-per-tool.

## What this page will cover

- The 401 + WWW-Authenticate flow: server signals "I require auth"
- OAuth 2.x with PKCE for public clients: the dance from authorization request to bearer token
- Where the token lives: HTTP `Authorization: Bearer ...` on every subsequent request
- PRM (Protected Resource Metadata): server publishes its auth requirements at a well-known URI
- JWT validation: signature, claims, audience, exp/nbf
- Fine-grained-auth-per-tool: tagging tools with required scopes, runtime check at dispatch
- mcpkit's retry semantics on auth failures (`client_auth_retry_test.go`)
- Why stdio doesn't need this (process-isolation trust boundary)

## Next to read

*(Terminal — auth is off the main learning path. For the implementation reference, see `ext/auth/docs/DESIGN.md`.)*
