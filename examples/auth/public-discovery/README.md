# Pre-Auth Public Discovery

Server with JWT auth that allows clients to discover tools before authenticating. `tools/list` works without a token, but `tools/call` still requires one.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `server.WithAuth`, `server.WithPublicMethods` |
| Extension | `ext/auth` — `JWTValidator`, `MountAuth` |

## Setup

```bash
cd examples/auth
go run ./public-discovery
```

The server prints a token. Connect to `http://localhost:8085/mcp`.

## Exercises

Connect **without** a token:

- `initialize` — works
- `tools/list` — works, shows available tools
- `ping` — works

```
Echo hello
```

- Returns **401** — tool execution requires auth

Now connect **with** the printed token:

```
Echo hello
```

- Returns: `echo: hello (user: alice, scopes: [read])` — everything works

## Screenshots

### tools/list works without a token — discover available tools


### Calling echo without a token — 401 unauthorized


## Key Files

| File | What |
|------|------|
| `main.go` | Server with `WithPublicMethods("initialize", "tools/list", ...)` |
| `../common/setup.go` | In-process AS, echo tools |
