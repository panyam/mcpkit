# AppHost — Host-Side App Management

Demonstrates AppHost mediating between an MCP server and an app bridge with bidirectional tool calls.

## What you'll learn

- **Create MCP server with tools** — The server provides two tools: echo (returns input) and time (returns current time).
- **Connect client to server via in-process transport** — The client connects without HTTP — using InProcessTransport for direct dispatch.
- **Create InProcessAppBridge with app-provided tools** — The bridge simulates an MCP App (iframe). It registers two tools that the host/model can call directly.
- **Create AppHost and wire everything together** — AppHost wires up bidirectional routing and fetches the initial app tool list.
- **ListAllTools — aggregated server + app tools** — ListAllTools merges tools from the MCP server and the app bridge into a single list.
- **CallAppTool — host invokes an app-provided tool** — The host calls a tool registered by the app. The bridge dispatches to the Go handler.
- **App calls server tool via bridge → AppHost → Client** — The app calls a server-side tool through the bridge. AppHost forwards to the MCP server via the Client.
- **Dynamic registration — app adds a tool at runtime** — The app registers a new tool after startup. AppHost detects the change and refreshes its cache.

## Flow

```mermaid
sequenceDiagram
    participant Srv as MCP Server
    participant Client as MCP Client
    participant Host as AppHost
    participant Bridge as InProcessAppBridge

    Note over Srv,Bridge: Step 1: Create MCP server with tools
    Srv->>Srv: RegisterTool("server_echo")
    Srv->>Srv: RegisterTool("server_time")

    Note over Srv,Bridge: Step 2: Connect client to server via in-process transport
    Client->>Srv: initialize
    Srv-->>Client: capabilities, serverInfo

    Note over Srv,Bridge: Step 3: Create InProcessAppBridge with app-provided tools
    Bridge->>Bridge: RegisterTool("app_greet")
    Bridge->>Bridge: RegisterTool("app_counter")

    Note over Srv,Bridge: Step 4: Create AppHost and wire everything together
    Host->>Bridge: SetRequestHandler (app→host)
    Host->>Bridge: SetNotificationHandler (list_changed)
    Host->>Bridge: Start()
    Host->>Bridge: Send(tools/list) — initial fetch
    Bridge-->>Host: {tools: [app_greet, app_counter]}

    Note over Srv,Bridge: Step 5: ListAllTools — aggregated server + app tools
    Host->>Client: ListTools() — server tools
    Client-->>Host: [server_echo, server_time]
    Host->>Bridge: cached app tools
    Bridge-->>Host: [app_greet, app_counter]

    Note over Srv,Bridge: Step 6: CallAppTool — host invokes an app-provided tool
    Host->>Bridge: Send(tools/call, {name: "app_greet", args: {name: "World"}})
    Bridge-->>Host: ToolResult {text: "Hello, World!"}

    Note over Srv,Bridge: Step 7: App calls server tool via bridge → AppHost → Client
    Bridge->>Host: SendToHost(tools/call, {name: "server_echo"})
    Host->>Client: Call(tools/call, params)
    Client->>Srv: JSON-RPC tools/call
    Srv-->>Client: ToolResult
    Client-->>Host: CallResult
    Host-->>Bridge: Response

    Note over Srv,Bridge: Step 8: Dynamic registration — app adds a tool at runtime
    Bridge->>Bridge: RegisterTool("app_dice")
    Bridge->>Host: notifications/tools/list_changed
    Host->>Bridge: Send(tools/list) — refresh
    Bridge-->>Host: {tools: [app_greet, app_counter, app_dice]}
```

## Steps

### Step 1: Create MCP server with tools

> **References:** [MCP Specification](https://spec.modelcontextprotocol.io)

The server provides two tools: echo (returns input) and time (returns current time).

### Step 2: Connect client to server via in-process transport

The client connects without HTTP — using InProcessTransport for direct dispatch.

### Step 3: Create InProcessAppBridge with app-provided tools

> **References:** [MCP Apps Extension](https://modelcontextprotocol.io/extensions/apps/overview)

The bridge simulates an MCP App (iframe). It registers two tools that the host/model can call directly.

### Step 4: Create AppHost and wire everything together

AppHost wires up bidirectional routing and fetches the initial app tool list.

### Step 5: ListAllTools — aggregated server + app tools

ListAllTools merges tools from the MCP server and the app bridge into a single list.

### Step 6: CallAppTool — host invokes an app-provided tool

The host calls a tool registered by the app. The bridge dispatches to the Go handler.

### Step 7: App calls server tool via bridge → AppHost → Client

The app calls a server-side tool through the bridge. AppHost forwards to the MCP server via the Client.

### Step 8: Dynamic registration — app adds a tool at runtime

The app registers a new tool after startup. AppHost detects the change and refreshes its cache.

### Cleanup

AppHost.Close() closes the bridge. The caller closes the Client separately.
In a real application, you'd defer these in the appropriate scope.

## References

- [MCP Specification](https://spec.modelcontextprotocol.io)
- [MCP Apps Extension](https://modelcontextprotocol.io/extensions/apps/overview)

## Run it

```bash
go run ./examples/host/01-apphost/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/host/01-apphost/ --non-interactive
```

## What to verify

- **Step 5**: ListAllTools shows 4 tools (2 server + 2 app)
- **Step 6**: CallAppTool returns "Hello, World!" and counter increments to 2
- **Step 7**: App calls server_echo, result includes "echo: from the app"
- **Step 8**: After dynamic registration, 3 app tools listed (app_greet, app_counter, app_dice)
