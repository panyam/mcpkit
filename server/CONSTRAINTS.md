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

## C4: No spec extensions without WG/IG consensus
Do not invent new protocol methods, routing schemes, wildcard syntaxes, or
capability fields that go beyond the current MCP spec. If a feature requires
extending the wire protocol (new methods, new message shapes, new matching
semantics), it should go through the MCP Working Group / Interest Group first.

mcpkit should implement the spec faithfully. Server-side convenience helpers
(e.g., handler-accessible wrappers around existing spec methods) are fine —
they don't change the wire protocol. But new semantics visible to clients
(e.g., `/*` wildcard subscriptions, custom notification routing) are
extensions and need spec-level agreement before shipping.

**When to push back:** if an issue proposes behavior that a conformant MCP
client would not understand without mcpkit-specific knowledge, flag it as a
potential extension and defer until the spec covers it.

**Verify:** review any new `resources/subscribe`, `notifications/*`, or
capability advertisement changes for spec conformance before merging.
