# Dashboard — MCP App with Tool Lifecycle

A data dashboard that registers 5 app-provided tools and demonstrates the full tool lifecycle: register, enable, disable, remove, and re-register. Tools become available based on app state (data loaded vs empty).

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `server.Run` |
| Extension | `ext/ui` — `UIExtension`, `RegisterTypedAppTool`, `BridgeTemplateDef` |
| Bridge | `MCPApp.registerTool()`, `handle.enable()`, `handle.disable()`, `handle.remove()`, `MCPApp.sendToolListChanged()` |

## What it demonstrates

- **5 app-provided tools** with different lifecycle behaviors
- **State-dependent availability**: `export_csv` and `set_filter` start disabled, enabled when data loads
- **Tool removal and re-registration**: `export_csv` can be removed and re-added via UI button
- **`sendToolListChanged()`** fires on every state change so the host stays in sync

## App-Provided Tools

| Tool | Initial State | Description |
|------|---------------|-------------|
| `query_data` | Active | Query dataset with optional category filter |
| `export_csv` | Disabled | Export current view as CSV (enabled when data loaded) |
| `refresh_data` | Active | Reload data from source |
| `set_filter` | Disabled | Apply filters (enabled when data loaded) |
| `get_settings` | Active | Read dashboard settings |

## Sequence Diagrams

### Model queries data (tool becomes fully active)

```mermaid
sequenceDiagram
    participant LLM
    participant Host
    participant App as Dashboard (iframe)

    Note over App: Initial state: export_csv DISABLED, set_filter DISABLED

    LLM->>Host: "Show me the hardware items"
    Host->>App: tools/call {name: "query_data", args: {category: "hardware"}}
    App->>App: Load data, filter by hardware
    App-->>Host: [{name: "Widget A", ...}, {name: "Gadget Z", ...}]
    
    Note over App: Data loaded → enable export_csv, set_filter
    App->>Host: notifications/tools/list_changed
    Host-->>LLM: 2 hardware items found
```

### Tool removal and re-registration

```mermaid
sequenceDiagram
    participant User
    participant App as Dashboard (iframe)
    participant Host

    User->>App: clicks "Toggle Export Tool"
    App->>App: handle.remove() — export_csv removed
    App->>Host: notifications/tools/list_changed
    Note over Host: tools/list now returns 4 tools

    User->>App: clicks "Toggle Export Tool" again
    App->>App: registerTool("export_csv", ...) — re-registered
    App->>Host: notifications/tools/list_changed
    Note over Host: tools/list now returns 5 tools
```

### Tool enable/disable based on state

```mermaid
sequenceDiagram
    participant User
    participant App as Dashboard (iframe)
    participant Host

    User->>App: clicks "Load Data"
    App->>App: export_csv.enable(), set_filter.enable()
    App->>Host: notifications/tools/list_changed
    Note over Host: export_csv and set_filter now callable

    User->>App: clicks "Clear Data"
    App->>App: export_csv.disable(), set_filter.disable()
    App->>Host: notifications/tools/list_changed
    Note over Host: export_csv and set_filter now rejected
```

## Setup

```bash
cd examples/apps/dashboard
go run . -addr :8080
```

## Connect a host

In MCPJam (or Claude Desktop):
1. Add server: `http://localhost:8080/mcp` (Streamable HTTP)
2. Server name: "Dashboard"

## Prompts to try

- "Open the dashboard" — opens the dashboard UI
- "Query hardware items" — calls `query_data` with category filter
- "Export the data as CSV" — calls `export_csv` (fails if no data loaded)
- "Load the data first, then export" — loads data, enables export, then exports
- "What are the dashboard settings?" — calls `get_settings`

## Key files

| File | What |
|------|------|
| `dashboard.html` | HTML with bridge + 5 `registerTool()` calls + lifecycle management |
| `main.go` | Go server: open_dashboard tool + resource serving |
