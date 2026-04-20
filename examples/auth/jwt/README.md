# JWT/JWKS Auth

Server validates RS256 JWTs via an in-process JWKS endpoint. The echo tool reports the authenticated user's identity and claims.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `server.WithAuth` |
| Extension | `ext/auth` — `JWTValidator`, `MountAuth` (PRM discovery endpoints) |

## Setup

```bash
cd examples/auth
go run ./jwt
```

The server prints a valid token on startup. Connect to `http://localhost:8082/mcp` with `Authorization: Bearer <token>`.

## Exercises

Connect with the token printed at startup:

```
Echo hello
```

- Returns: `echo: hello (user: alice, scopes: [read write])`

Now connect **without** a token:

```
Echo hello
```

- Returns **401**

Try a tampered or expired token — also **401**.

## Screenshots

### Echo reports authenticated identity from JWT claims

![JWT Auth](screenshots/jwt-auth.png)

## Key Files

| File | What |
|------|------|
| `main.go` | Server with JWT validator + PRM auth endpoints |
| `../common/setup.go` | In-process AS, JWT minting, echo tools |
