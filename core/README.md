# core

MCP protocol types and tool-handler APIs.

This package defines the shared types used by both `server/` and `client/`. It has **no dependencies** on either — only stdlib and encoding/json.

## What belongs here

- JSON-RPC types: `Request`, `Response`, `Error`
- MCP domain types: `ToolDef`, `ResourceDef`, `PromptDef`, `Content`, `Claims`
- Tool-handler APIs: `Sample()`, `Elicit()`, `EmitLog()`, `EmitProgress()`
- Interfaces: `Transport`, `TokenSource`, `AuthValidator`
- Protocol constants: `ServerInfo`, `ClientInfo`, `ClientCapabilities`

## What does NOT belong here

- Server implementation (Dispatcher, transports, middleware) → `server/`
- Client implementation (HTTP transports, reconnection) → `client/`
- Auth implementation (JWT, PRM, OAuth) → `ext/auth/`
- Anything that imports `server/` or `client/`
