# Session Binding (Hijacking Prevention)

Demonstrates that one user cannot use another user's MCP session. The server binds `Claims.Subject` to the session at creation time — subsequent requests with a different principal are rejected.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `server.WithAuth` (session binding is automatic) |
| Extension | `ext/auth` — `JWTValidator`, `MountAuth` |

Session binding is built into the Streamable HTTP transport. When `WithAuth` is configured, the transport automatically binds the JWT `sub` claim to the session on first request.

## Setup

```bash
cd examples/auth
go run ./session-binding
```

The server prints tokens for alice and bob. Connect to `http://localhost:8084/mcp`.

## Exercises

Connect with **alice's token**:

```
Echo hello
```

- Returns: `echo: hello (user: alice, scopes: [read])`
- Note the `Mcp-Session-Id` in the response headers

Now send a request with **bob's token** using alice's session ID:

- Returns **403 Forbidden** — session is bound to alice

Connect bob on a **fresh session** (no session ID header):

```
Echo hello
```

- Returns: `echo: hello (user: bob, scopes: [read])` — works fine, gets his own session

## Screenshots

### Alice connected — session bound to her identity

![Session Bound](screenshots/session-bound.png)

### Bob's token on alice's session — 403 rejected

![Hijack Rejected](screenshots/hijack-rejected.png)

## Key Files

| File | What |
|------|------|
| `main.go` | Server with two users, session binding via `WithAuth` |
| `../common/setup.go` | In-process AS, token minting |
