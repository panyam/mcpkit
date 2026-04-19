# Unified Auth — All Patterns in One Server

A single MCP server that layers all four auth patterns together. Start here to experience the full auth surface.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `server.WithAuth`, `server.WithPublicMethods`, `server.WithMux` |
| Extension | `ext/auth` — `JWTValidator`, `MountAuth` (PRM endpoints), `RequireScope` |
| Auth patterns | JWT/JWKS validation, public discovery, scope enforcement, session binding |

## Setup

```bash
cd examples/auth
go run ./unified
```

The server prints tokens for each exercise. Connect your MCP host to `http://localhost:8080/mcp` (Streamable HTTP).

## Exercises

### 1. Public Discovery

Connect **without** a token.

Try these prompts:

```
Echo hello
```

- `tools/list` succeeds — you can see the available tools
- But calling `echo` returns **401** — tool execution requires auth

### 2. JWT Authentication

Connect with the **read-only token** printed at startup.

```
Echo hello
```

- Returns: `echo: hello (user: alice, scopes: [read])`

### 3. Scope Enforcement

Still connected with the read-only token:

```
Call the write tool
```

- Returns: `error: insufficient scope: requires "write"`

```
Call the admin tool
```

- Returns: `error: insufficient scope: requires "admin"`

Reconnect with the **read+write token**:

```
Call the write tool
```

- Returns: `write ok`

```
Call the admin tool
```

- Still fails — missing `admin` scope

Reconnect with the **all-scopes token** — everything works.

### 4. Session Binding

Connect as alice (any token). Note the `Mcp-Session-Id` in the response headers.

Now send a request with **bob's token** using the same session ID — **403 Forbidden**.

## Screenshots

### Connected with a valid JWT — echo reports identity

![Unified Auth](screenshots/unified-auth.png)

### Calling write-tool with a read-only token — scope enforcement in action

![Scope Enforcement](screenshots/scope-enforcement.png)

### Using bob's token on alice's session — 403 rejected

![Session Binding Rejection](screenshots/session-binding.png)

## Key Files

| File | What |
|------|------|
| `main.go` | Server: JWT + public discovery + scopes + session binding |
| `../common/setup.go` | Shared auth infra: in-process AS, JWT minting, echo tools |
