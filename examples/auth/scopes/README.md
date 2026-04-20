# Scope Enforcement

Server has tools requiring different scopes. Demonstrates step-up authorization — try calling a tool you don't have permission for, then reconnect with a broader token.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `server.WithAuth` |
| Extension | `ext/auth` — `JWTValidator`, `MountAuth`, `RequireScope` |

## Setup

```bash
cd examples/auth
go run ./scopes
```

The server prints three tokens with different scope sets. Connect to `http://localhost:8083/mcp`.

## Exercises

Connect with the **read-only** token:

```
Echo hello
```

- Returns: `echo: hello (user: alice, scopes: [read])` — works, no scope required

```
Call the write tool
```

- Returns: `error: insufficient scope: requires "write"`

```
Call the admin tool
```

- Returns: `error: insufficient scope: requires "admin"`

Reconnect with the **read+write** token:

```
Call the write tool
```

- Returns: `write ok`

```
Call the admin tool
```

- Still fails — missing `admin` scope

Reconnect with the **all-scopes** token — everything works.

## Screenshots

### Calling write-tool with a read-only token — denied

![Scope Denied](screenshots/scope-denied.png)

### Reconnected with read+write token — granted

![Scope Granted](screenshots/scope-granted.png)

## Key Files

| File | What |
|------|------|
| `main.go` | Server with scope-protected tools |
| `../common/setup.go` | Echo tools with `auth.RequireScope` checks |
