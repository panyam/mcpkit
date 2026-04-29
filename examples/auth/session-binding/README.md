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

Testing session binding requires manually crafting HTTP requests with mismatched tokens and session IDs — a normal MCP host manages the session header automatically, so you can't trigger a hijack through the UI.

### Step 1: Connect as alice

```bash
curl -s -D- http://localhost:8084/mcp \
  -H "Authorization: Bearer <alice-token>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
```

Note the `Mcp-Session-Id` header in the response (e.g. `Mcp-Session-Id: abc123`).

### Step 2: Call echo as alice — works

```bash
curl -s http://localhost:8084/mcp \
  -H "Authorization: Bearer <alice-token>" \
  -H "Mcp-Session-Id: <session-id-from-step-1>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello"}}}'
```

Returns: `echo: hello (user: alice, scopes: [read])`

### Step 3: Try bob's token on alice's session — rejected

```bash
curl -s -D- http://localhost:8084/mcp \
  -H "Authorization: Bearer <bob-token>" \
  -H "Mcp-Session-Id: <session-id-from-step-1>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hijack"}}}'
```

Returns **403 Forbidden** — the session is bound to alice's `sub` claim.

### Step 4: Bob on a fresh session — works

```bash
curl -s -D- http://localhost:8084/mcp \
  -H "Authorization: Bearer <bob-token>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
```

Bob gets his own session — no conflict with alice's.

## Screenshots

### Alice connected — session bound to her identity


### Bob's token on alice's session — 403 rejected


## Key Files

| File | What |
|------|------|
| `main.go` | Server with two users, session binding via `WithAuth` |
| `../common/setup.go` | In-process AS, token minting |
