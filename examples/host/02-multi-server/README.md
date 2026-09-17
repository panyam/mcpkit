========================================================================
  Multi-Server Registry
  Demonstrates ServerRegistry managing 3 MCP servers with tool aggregation, collision resolution, and app bridge integration.
  9 steps
========================================================================

  Step 1: Create 3 MCP servers with overlapping tools
  ----------------------------------------------------------------------
    Refs: MCP Specification
    W ->> W: get_forecast, get_alerts, get_info
    C ->> C: list_events, create_event, get_info
    K ->> K: get_time

    Weather and Calendar both have a 'get_info' tool, which will cause a
    collision in the registry.

    Press Enter to run this step...
  Created 3 servers: weather (3 tools), calendar (3 tools), clock (1 tool)
  Note: 'get_info' exists in both weather and calendar

  Step 2: Create ServerRegistry with ToolResolver and CollisionHandler
  ----------------------------------------------------------------------
    Refs: mcpkit APPS_HOST.md
    Reg ->> Reg: WithToolResolver(arg-based routing)
    Reg ->> Reg: WithCollisionHandler(log collisions)

    The resolver picks a server based on args. The collision handler
    logs when ambiguity is detected.

    Press Enter to run this step...
  Registry created with arg-based resolver and collision logger

  Step 3: Add all 3 servers, and a collision is detected
  ----------------------------------------------------------------------
    Reg ->> W: Add("weather", weatherClient)
    Reg ->> C: Add("calendar", calendarClient)
    Reg ->> K: Add("clock", clockClient)

    When calendar is added, the registry detects that 'get_info' now
    exists in both weather and calendar.

    Press Enter to run this step...
  Added weather
    ⚠ Collision detected: 'get_info' in servers [weather, calendar]
  Added calendar
  Added clock
  Servers: [calendar clock weather]

  Step 4: AllTools, the aggregated tool list with routing metadata
  ----------------------------------------------------------------------
    Reg ->> Reg: AllTools()

    Returns all tools from all servers. Each tool has clean name +
    ServerID metadata.

    Press Enter to run this step...
  7 tools across 3 servers:
    create_event     server=calendar    Create a calendar event
    get_info         server=calendar    Get calendar service info
    list_events      server=calendar    List calendar events
    get_time         server=clock       Get current time
    get_alerts       server=weather     Get weather alerts
    get_forecast     server=weather     Get weather forecast
    get_info         server=weather     Get weather service info

  Step 5: CallTool (unambiguous), which routes directly
  ----------------------------------------------------------------------
    Reg ->> W: CallTool("get_forecast")
    W -->> Reg: ToolResult

    get_forecast exists only in weather, so no resolver is needed and it
    routes directly.

    Press Enter to run this step...
  Result: [weather] get_forecast: ok

  Step 6: CallTool (ambiguous), where the resolver is invoked
  ----------------------------------------------------------------------
    Reg ->> Reg: CallTool("get_info", {source: "calendar"})
    Reg ->> Reg: Resolver picks calendar
    Reg ->> C: tools/call
    C -->> Reg: ToolResult

    get_info is ambiguous (weather + calendar). The resolver sees
    {source: "calendar"} and picks calendar.

    Press Enter to run this step...
  Result: [calendar] get_info: ok

  Step 7: CallToolOn, where explicit routing bypasses the resolver
  ----------------------------------------------------------------------
    Reg ->> W: CallToolOn("weather", "get_info")
    W -->> Reg: ToolResult

    CallToolOn routes directly to the specified server. No resolver
    involved.

    Press Enter to run this step...
  Result: [weather] get_info: ok

  Step 8: Remove server, and its tools disappear from the index
  ----------------------------------------------------------------------
    Reg ->> K: Remove("clock")

    After removing clock, get_time is no longer available.

    Press Enter to run this step...
  Remaining servers: [calendar weather]
  CallTool("get_time"): unknown tool "get_time" ✓

  Step 9: AddWithBridge, a server with app-provided tools
  ----------------------------------------------------------------------
    Refs: MCP Apps Extension
    Reg ->> K: AddWithBridge("clock-v2", client, bridge)
    Br ->> Br: RegisterTool("app_stopwatch")

    Re-adds clock with an app bridge that provides an extra tool. Both
    server and app tools appear in AllTools.

    Press Enter to run this step...
  Servers: [calendar clock-v2 weather]
  All tools (8):
    create_event     server=calendar    source=server  Create a calendar event
    get_info         server=calendar    source=server  Get calendar service info
    list_events      server=calendar    source=server  List calendar events
    app_stopwatch    server=clock-v2    source=app     Start/stop a stopwatch
    get_time         server=clock-v2    source=server  Get current time (v2)
    get_alerts       server=weather     source=server  Get weather alerts
    get_forecast     server=weather     source=server  Get weather forecast
    get_info         server=weather     source=server  Get weather service info
  Called app_stopwatch → stopwatch: 00:00:05

=== Done ===
