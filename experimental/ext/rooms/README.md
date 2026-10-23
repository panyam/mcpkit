# experimental/ext/rooms

Tools offered by web pages, called by agents over MCP. Pre-SEP and experimental; the design is in
issue [#1581](https://github.com/panyam/mcpkit/issues/1581).

An agent cannot call a web page: MCP only lets a client call a server. The room bridge joins the
relay room for a resource (`design:demo/board.kicad_sch`, `review:42`), lists the tools of every
page in it, and serves them to the agents attached to that room. Each call goes to one page.

```
agent (MCP client) --MCP--> rooms.Bridge (MCP server) --relay room--> page (tools)
```

## Serving the bridge

```go
relay := memroom.New() // or massrelay.Embedded / massrelay.Dial, see below
bridge := rooms.New(relay.Transport("bridge"))
srv := server.NewServer(info, server.WithAuth(jwtValidator))
bridge.Register(srv)
defer bridge.Close()
srv.Run(":8080")
```

`Register` adds three tools and two middlewares. The bridge needs the session wire, because
attachments and pairings are per MCP session.

| Tool | Scope | What it does |
|------|-------|--------------|
| `attach(resource)` | `rooms:attach` | Joins this session to the resource's room. Returns a pairing code and the roster. |
| `detach(resource)` | `rooms:attach` | Leaves, ending this session's pairings there. |
| `list_pages(resource?)` | `pages:read` | Page id, user, label, last focus, `allowed` and `paired` for this session, tools. |
| each page tool | `pages:read` if the page marks it read-only, else `pages:act` | Runs on one page. Takes an optional `page` argument. |

A session sees the page tools of the rooms it attached to and no others. A missing scope is a 403
with an `insufficient_scope` challenge (`WithResourceMetadataURL` sets its link).

`WithLocalSubject("me")` is single-user local mode for a server with no identity provider: a
session without claims acts for `me`, so the page's switch is the only step.

## Relays

`RoomTransport` is the bridge's only view of the relay. `memroom` is in-process and in-memory, for
tests and for a bridge whose pages share its process. A real deployment uses massrelay through
[`experimental/ext/rooms/massrelay`](massrelay/), a separate module so this one never depends on it:

```go
bridge := rooms.New(massrelay.Embedded(relayApp.Service, &services.Principal{Subject: "bridge"}))
page, _ := rooms.JoinPage(ctx, massrelay.Dial("https://relay.example.com", relayToken), room)
```

## Offering tools from a page

A browser page uses the TypeScript client in [`ts/`](ts/), `joinToolRoom`, which takes WebMCP's
`registerTool` shape. A Go program that shows something uses `rooms.Page`:

```go
page, _ := rooms.JoinPage(ctx, relay.Transport("alice"), "design:demo/board.kicad_sch")
page.RegisterTool(rooms.PageTool{
    Name:        "show_sheet",
    Description: "Open a sheet of the design on screen",
    InputSchema: map[string]any{"type": "object", "properties": map[string]any{"sheet": map[string]any{"type": "string"}}},
    Execute: func(ctx context.Context, call rooms.PageCall) (core.ToolResult, error) {
        return core.TextResult("opened"), nil
    },
})
page.SetAgentsAllowed(true) // "let my agents drive this tab"
```

A tool may not declare a property named `page`, which the bridge adds. The first page to register
a name fixes its schema across the bridge, and a page registering the same name with a different
schema has that tool refused. Either refusal reaches the page as `notifications/rooms/tool_refused`.

## Which page a call goes to

1. The `page` argument, when given.
2. Else the page paired with this session in that room, which is the page that agreed most recently.
3. Else, when one page in the room has agreed to this session, that page.
4. Else an error result carrying the roster, so the model can choose.

## Agreement

A page's user agrees before an agent can call it.

- **Same user.** `SetAgentsAllowed(true)` agrees to every session whose subject is the page's user.
- **Another user.** `attach` returns a code. The page's user accepts it with `AcceptPairing(code)`.
  `OnPairingRequest` shows the page who asked.
- **Ending it.** `Revoke(session)` on the page, `detach`, the page leaving, or the MCP session
  closing (the bridge hooks the server's `OnSessionClose`).

## Calls

The bridge gives each call its own id on the relay, since an agent's request id is unique only in
its session. A page that leaves fails its pending calls at once; a page that does not answer fails
at `WithCallTimeout` (30s by default) and is sent `notifications/cancelled`. An agent's cancel
reaches the page's `Execute` as a cancelled context, and the page's progress reaches the agent under
the agent's own progress token.
