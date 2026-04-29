# Bearer Token Auth

The simplest auth pattern — server validates a static bearer token. No JWT, no JWKS, no authorization server.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `server.WithBearerToken` |

## Setup

```bash
cd examples/auth
go run ./bearer
```

Connect to `http://localhost:8081/mcp` with header `Authorization: Bearer my-secret-token`.

## Exercises

Connect with `Authorization: Bearer my-secret-token`:

```
Echo hello
```

- Returns: `echo: hello (anonymous)` — bearer auth doesn't propagate identity

Now connect **without** a token or with a wrong token:

```
Echo hello
```

- Returns **401** — all calls fail

## Screenshots

### Connected with the static token — echo responds


## Key Files

| File | What |
|------|------|
| `main.go` | Server with `WithBearerToken("my-secret-token")` |
| `../common/setup.go` | Shared echo tools |
