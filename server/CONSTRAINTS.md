# server/ Constraints

## C1: No client imports
server/ must NOT import `client/`. The dependency graph is: core ← server, core ← client.

**Verify:** `grep -r '"github.com/panyam/mcpkit/client"' server/*.go`
**Expected:** no output (test files may import client for integration tests)

## C2: All exported types that both server and client need go in core/
If you're adding a type that the client also needs, put it in core/, not here.
Examples: Request, Response, ToolDef, ServerInfo.

## C3: Transport implementations are server-internal
SSE, Streamable HTTP, stdio, and InProcessTransport live here.
They satisfy `core.Transport` but their internals (hub, session map, etc.) are unexported.
