# Dice Roller — Vanilla JS MCP App

A minimal MCP App using the bridge with plain JavaScript. No framework, no build step.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `core.TextTool`, `server.Run` |
| Extension | `ext/ui` — `UIExtension`, `RegisterTypedAppTool`, `BridgeTemplateDef`, `NewBridgeData` |
| MCP primitives | Tools, Resources (App resource via `ui://` URI) |

## What it demonstrates

- Bridge included via Go `html/template` (`{{ template "mcpkit-bridge" .Bridge }}`)
- `MCPApp.on('toolresult', ...)` to display results from LLM-initiated tool calls
- `MCPApp.callTool('roll_dice', ...)` for iframe-initiated tool calls (Roll Again button)
- `MCPApp.on('connected', ...)` for connection status
- Host theme auto-applied by the bridge

## App-Provided Tools

The dice app registers two tools that the host/model can call directly:

| Tool | Description |
|------|-------------|
| `roll_dice` | Roll a die (optional `sides` param, default 6) |
| `get_last_roll` | Get the result of the last roll |

These use `MCPApp.registerTool()` — the app auto-handles `tools/list` and `tools/call` from the host.

## Sequence Diagrams

### LLM-initiated dice roll (server tool)

```mermaid
sequenceDiagram
    participant LLM
    participant Host
    participant Server as Go Server
    participant App as Dice App (iframe)

    LLM->>Host: "Roll a d20"
    Host->>Server: tools/call {name: "roll_dice", args: {sides: 20}}
    Server-->>Host: ToolResult {text: "Rolled d20: 17"}
    Host->>App: ui/notifications/tool-result
    App->>App: showResult("Rolled d20: 17")
    Host-->>LLM: "Rolled d20: 17"
```

### User clicks "Roll Again" (app→host→server)

```mermaid
sequenceDiagram
    participant User
    participant App as Dice App (iframe)
    participant Host
    participant Server as Go Server

    User->>App: clicks "Roll Again"
    App->>Host: MCPApp.callTool("roll_dice", {sides: 6})
    Host->>Server: tools/call
    Server-->>Host: ToolResult
    Host-->>App: response
    App->>App: showResult("Rolled d6: 4")
```

### Host queries app tool (app-provided)

```mermaid
sequenceDiagram
    participant LLM
    participant Host
    participant App as Dice App (iframe)

    LLM->>Host: "What was the last roll?"
    Host->>App: tools/call {name: "get_last_roll"}
    App-->>Host: {text: "Last roll: d6 = 4"}
    Host-->>LLM: "Last roll: d6 = 4"
```

## Setup

```bash
cd examples/apps/vanilla
go run . -addr :8080
```

## Connect a host

In MCPJam (or Claude Desktop):
1. Add server: `http://localhost:8080/mcp` (Streamable HTTP)
2. Server name: "Dice App"

## Prompts to try

- "Roll a die" — calls `roll_dice`, result appears in the iframe
- "Roll a d20" — calls with `sides: 20`
- Then click **Roll Again** in the iframe — calls the tool back through the bridge

## Screenshots

![Dice Roller](screenshots/dice-roller.png)

## Key files

| File | What |
|------|------|
| `dice.html` | HTML template with bridge + app logic in one `<script type="module">` |
| `main.go` | Go server: parses template, registers tool + resource, serves MCP |
