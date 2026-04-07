# client/ Constraints

## C1: No server imports
client/ must NOT import `server/`. Use `core.Transport` interface for in-process testing.

**Verify:** `grep -r '"github.com/panyam/mcpkit/server"' client/*.go`
**Expected:** no output (test files in `package client_test` may import server)

## C2: Transport-agnostic
The Client struct works with any `core.Transport`. HTTP-specific code lives in
the transport implementations (sseClientTransport, streamableClientTransport),
not in the Client methods.

## C3: Auth retry is transport-level
`DoWithAuthRetry` handles 401/403 at the HTTP transport level. The Client API
(ToolCall, ReadResource, etc.) is unaware of auth — it delegates to the transport.
