========================================================================
  AppHost and host-side app management
  Demonstrates AppHost mediating between an MCP server and an app bridge with bidirectional tool calls.
  8 steps
========================================================================

  Step 1: Create MCP server with tools
  ----------------------------------------------------------------------
    Refs: MCP Specification
    Srv ->> Srv: RegisterTool("server_echo")
    Srv ->> Srv: RegisterTool("server_time")

    The server provides two tools: echo (returns input) and time
    (returns current time).

    Press Enter to run this step...
  Server created with 2 tools: server_echo, server_time

  Step 2: Connect client to server via in-process transport
  ----------------------------------------------------------------------
    Client ->> Srv: initialize
    Srv -->> Client: capabilities, serverInfo

    The client connects without HTTP, using InProcessTransport for
    direct dispatch.

    Press Enter to run this step...
  Connected to demo-server 1.0
  Server tools: server_echo, server_time

  Step 3: Create InProcessAppBridge with app-provided tools
  ----------------------------------------------------------------------
    Refs: MCP Apps Extension
    Bridge ->> Bridge: RegisterTool("app_greet")
    Bridge ->> Bridge: RegisterTool("app_counter")

    The bridge simulates an MCP App (iframe). It registers two tools
    that the host/model can call directly.

    Press Enter to run this step...
  Bridge created with 2 app tools: app_greet, app_counter

  Step 4: Create AppHost and wire everything together
  ----------------------------------------------------------------------
    Host ->> Bridge: SetRequestHandler (app→host)
    Host ->> Bridge: SetNotificationHandler (list_changed)
    Host ->> Bridge: Start()
    Host ->> Bridge: Send(tools/list), initial fetch
    Bridge -->> Host: {tools: [app_greet, app_counter]}

    AppHost wires up bidirectional routing and fetches the initial app
    tool list.

    Press Enter to run this step...
  AppHost started: bridge handlers wired, initial tool list fetched

  Step 5: ListAllTools, the aggregated server + app tools
  ----------------------------------------------------------------------
    Host ->> Client: ListTools(), server tools
    Client -->> Host: [server_echo, server_time]
    Host ->> Bridge: cached app tools
    Bridge -->> Host: [app_greet, app_counter]

    ListAllTools merges tools from the MCP server and the app bridge
    into a single list.

    Press Enter to run this step...
  All tools (4 total):
    - server_echo (Echo back the input)
    - server_time (Get current time)
    - app_counter (Increment and return a counter)
    - app_greet (Greet someone by name)

  Step 6: CallAppTool, where the host invokes an app-provided tool
  ----------------------------------------------------------------------
    Host ->> Bridge: Send(tools/call, {name: "app_greet", args: {name: "World"}})
    Bridge -->> Host: ToolResult {text: "Hello, World!"}

    The host calls a tool registered by the app. The bridge dispatches
    to the Go handler.

    Press Enter to run this step...
  Result: Hello, World!
  Counter after 2 calls: counter = 2

  Step 7: App calls server tool via bridge → AppHost → Client
  ----------------------------------------------------------------------
    Bridge ->> Host: SendToHost(tools/call, {name: "server_echo"})
    Host ->> Client: Call(tools/call, params)
    Client ->> Srv: JSON-RPC tools/call
    Srv -->> Client: ToolResult
    Client -->> Host: CallResult
    Host -->> Bridge: Response

    The app calls a server-side tool through the bridge. AppHost
    forwards to the MCP server via the Client.

    Press Enter to run this step...
  App called server_echo → echo: from the app

  Step 8: Dynamic registration, where the app adds a tool at runtime
  ----------------------------------------------------------------------
    Bridge ->> Bridge: RegisterTool("app_dice")
    Bridge ->> Host: notifications/tools/list_changed
    Host ->> Bridge: Send(tools/list), refresh
    Bridge -->> Host: {tools: [app_greet, app_counter, app_dice]}

    The app registers a new tool after startup. AppHost detects the
    change and refreshes its cache.

    Press Enter to run this step...
  App tools after dynamic registration (3):
    - app_counter
    - app_dice
    - app_greet
  Called app_dice → rolled: 4

  --- Cleanup ---
    AppHost.Close() closes the bridge. The caller closes the Client
    separately.
    In a real application, you'd defer these in the appropriate scope.

=== Done ===
