# core/ Constraints

## C1: No server or client imports
core/ must NOT import `server/` or `client/`. It is the leaf dependency.

**Verify:** `grep -r '"github.com/panyam/mcpkit/server"\|"github.com/panyam/mcpkit/client"' core/`
**Expected:** no output

## C2: No net/http dependency (except interfaces)
core/ should only use net/http for interface types (AuthValidator, ClaimsProvider).
No HTTP handlers, servers, or clients.

## C3: Types are protocol-level
Only MCP protocol types and tool-handler APIs belong here. Implementation logic
(routing, transport, session management) belongs in server/ or client/.
