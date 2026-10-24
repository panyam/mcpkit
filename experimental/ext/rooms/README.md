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

`Register` adds the bridge's tools and two middlewares. Every call names the room it is about, so
none of them needs an MCP session. On the session wire, `attach` also sets a default room.

| Tool | Scope | What it does |
|------|-------|--------------|
| `attach(resource)` | `rooms:attach` | Joins the room and returns a pairing code and the roster. On a session, makes the resource a default room. Never required. |
| `detach(resource)` | `rooms:attach` | Drops a default room. Pages that agreed to you still do. |
| `list_pages(resource?)` | `pages:read` | One room's tabs: page id, user, label, last focus, `allowed` and `default` for you, tools. |
| `pair(resource, code)` | `rooms:attach` | Pairs with the page showing `code`; see Agreement. |
| each page tool | `pages:read` if the page marks it read-only, else `pages:act` | Runs on one page. Takes optional `room` and `page` arguments. |

A page tool call names its room with the `room` argument the bridge adds, or, when the host's tools
already name what they act on, through `WithRoomOf(func(args) (room, error))`: agni maps its
`design` argument to `design:<mount>/<entry>`, and the arguments reach the page unchanged. A call
that names no room uses the session's default rooms, or fails asking for one.

### Declared page tools, and the stateless wire

`WithPageTools([]PageToolDecl{...})` fixes the bridge's catalog up front: name, description, input
schema and read-only hint. `tools/list` then answers the same for every caller, on either wire,
before the bridge has joined any room. That makes it cacheable (`server.WithListTTLMs`), and it needs
no `list_changed`. Tabs still register and unregister tools at runtime, and a call goes only to a
tab offering the tool right now. A page that registers an undeclared tool, or a declared one with a
different schema or read-only hint, has it refused (`tool_refused`). A declaration whose name is the
bridge's own, or whose schema has `page` (or `room` without `WithRoomOf`), makes `Register` panic.

Without declared tools the bridge keeps a dynamic registry: it has no page tools until it joins a
room (`attach`, `list_pages(resource)` or `pair`), and on the session wire a session sees the page
tools of its default rooms and of the pages that agreed to it, with `list_changed` as they come and
go. A caller with no session sees none of them, and a call to one is refused, naming
`WithPageTools`.

`WithDynamicCatalogTTL(ttl)` serves the dynamic registry to callers with no session too. Their
`tools/list` shows the tools of the pages that agreed to them, in rooms the bridge has joined, with
`ttlMs` set to `ttl` and `cacheScope: private`. The stateless wire has no `list_changed`, so a tool a
page registers can take up to one `ttl` to show, and every stateless client re-lists that often.
Keep it to a few seconds; a fixed catalog is cheaper whenever one will do.

### Limiting what an agent can do

- **The page registers only what its user may use.** That's the main way to restrict tools. A tab
  whose user can't edit doesn't offer `move_part`, so no call reaches it, and on the session wire
  the agent doesn't see the tool at all.
- **The grant is the gate for other users.** A page that doesn't grant Bob's agent never gets a call
  from it.
- **The caller reaches the page on every call**, as `PageCall.Caller` (TS: `ctx.caller`), the
  subject and client from the bridge's validated token, so a page can refuse per caller, e.g. let a
  paired user view a design but not edit it.
- **`rooms.TrustBridges(subjects...)`** on `JoinPage` makes a page answer only peers whose relay
  subject is one of the host's bridges. Without it, any holder of a relay token for the room can
  call the page's tools directly, past agreement, and claim any caller. Set it before relying on
  `Caller`. (Not in the TS client yet: massrelay's `CollabClient` doesn't pass peers' relay identity
  through.)
- **Scopes.** Read-only page tools need `pages:read` and the rest `pages:act`. A missing scope is a
  403 with an `insufficient_scope` challenge (`WithResourceMetadataURL` sets its link), on both
  wires. The list shows every tool by default, as MCP's step-up flow expects.
  `WithScopeFilteredList()` shows each caller only what its scopes allow, with `cacheScope: private`,
  and `list_pages` then carries `stepUp: [{tool, scopes}]` for the tools it hid.

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

1. The `page` argument, when given, if that page agreed to you.
2. Else the tab that agreed to you which your own user focused most recently. A page reports focus
   with `Focus()`, and a tab focused before a bridge joined keeps its place.
3. Else the only tab that agreed to you.
4. Else an error result carrying the room's roster, so the model can choose.

## Reaching many tabs

Two ways, depending on who decides.

**An agent asks, with a fan-out tool.** Declare it with `FanOut: true` in `WithPageTools`. A call
names its room and goes to every tab there that offers the tool and agreed to the caller, so
fan-out never reaches a tab that didn't agree. Instead of `page` the tool takes
`targets: {users: [...]}`, which narrows it to those users' tabs. The answer's structured content
is `{results: [FanOutResult]}`, one per tab with its page, user, label and either its result or
its error. Each tab has its own deadline, so a slow one doesn't hold up the rest.

A fan-out tool has to be `ReadOnly` (`Register` panics otherwise), since one call moving every
user's screen at once is what #1581 argued against. A call that would reach more than 25 tabs
(`WithFanOutLimit`) is refused, and told to narrow with `targets`.

**The host decides, with `Bridge.Notify`.** For things like "the design changed" or "it's these
users' birthday", where no agent is involved:

```go
rep, err := bridge.Notify(ctx, rooms.Selector{Users: []string{"alice"}}, "design.changed", map[string]any{"rev": 7})
```

`Selector.Rooms` empty means every room the bridge has joined, and a room named that it hasn't
joined is joined first. Notify can't find a user's tabs in rooms the bridge was never asked
about, since only the relay knows those. Pages handle it with `OnNotify(name, fn)`. The name and
data follow the same rules as page events. Being the host talking to its own pages, it needs no
agreement, though a page using `TrustBridges` hears it only from the host's bridges. Each room
gets one relay message addressed to every chosen tab in it (`Room.SendEach`), and a tab the relay
reports gone lands in `rep.Failed` with a `*NotMembersError` while the rest of its room is sent.

The massrelay transport paces each dialled connection at 25 messages a second
(`massrelay.WithSendRate`), under the relay's limit of 30, past which the relay drops messages
without saying so. A large fan-out takes longer rather than losing tabs. Fan-out tool calls stay
one message per tab, since each tab answers its own call.

## Agreement

A page's user agrees before an agent can call it. The page holds the agreement, as grants to a
principal: the subject of the caller's validated token and, optionally, its client. The page sends
its grants to every bridge that lists it, so several bridges in one room agree without sharing
state, and agreement needs no MCP session.

- **Same user.** `SetAgentsAllowed(true)` agrees to every agent acting for the page's own user.
- **Another user, agent's code.** `attach` returns a code (valid ten minutes). The page's user
  accepts it with `AcceptPairing(code)`. The bridge that minted it names the principal, and the page
  adds the grant. `OnPairingRequest` shows the page who asked.
- **Another user, page's code.** The page shows a code from `OfferPairingCode(ttl)`, and the user
  gives it to the agent, which enters it with `pair(resource, code)`. A code is single-use and lasts
  two minutes by default, at most ten. The bridge refuses a code shorter than six characters of the
  pairing alphabet, and stops checking a principal's guesses after five wrong ones in a minute.
- Either way `OnPaired` tells the page which agent it was (subject and client, from the token), and
  `Grants()` lists who it agrees to.
- **Ending it.** `Revoke(principal)` or `RevokeAll()` on the page, or the page leaving. A grant
  outlives the agent's MCP session: there is no session end on the stateless wire to hang it on.

### Which agents are around

A page can show its user which agents have used the tab, and offer to revoke them from there.
`OnPresence(fn)` runs on each bridge's report and `Presence()` returns the current list: one entry
per principal (subject, client), with `LastSeen` and `Acting`. An agent counts as soon as it
attaches to the page's room, with `Acting` false, and as acting once it calls the page. A page
hears of agents it never agreed to, the same as it sees their pairing requests.

Each bridge reports only the agents it served, so the page merges the reports: the latest
`LastSeen` wins, and acting on any bridge stays acting. An entry drops out an hour after it was
last seen (`WithPresenceTTL` on `JoinPage`). Reports name principals and never MCP sessions.

## Page events

A page tells the bridge when something happens: `Ask(text)` emits `room.ask`, `SelectionChanged(sel)`
emits `page.selection_changed`, and `Emit(name, data)` sends a name of the page's own
(`viewer.sheet_opened`). Names are one to four dot-separated lowercase segments, and data is capped at
`MaxEventDataBytes` (16 KiB). The page refuses anything else with `ErrInvalidEvent`, and the bridge
drops it too, since a page is any peer with a relay token.

The bridge hands each event to `WithPageEvents(fn)`, stamped with the page's peer id, its subject from
the relay token, and its label. It applies no agreement there. To offer the events to agents as MCP
Events (SEP-3415), with the bridge's agreement deciding who hears which page, use
[`experimental/integrations/roomsevents`](../../integrations/roomsevents), which also has the Claude
Code channel front end. `Attached(session, resource)` and `PageAgreed(session, resource, page)` are
the two questions it asks.

## Agents whose host speaks no events

`WithEventFallbacks(buffer)` adds two ways to hear page events without MCP Events, both applying the
same agreement:

- **`next_request(resource?, after?, wait_seconds?)`**, a tool that works with no session when it
  names its resource, and holds the call until the next
  event from a page that agreed to the session, or returns `event: null` after the wait (30 s by
  default, 120 at most). Pass the returned `cursor` as `after` and nothing between calls is missed,
  up to `buffer` recent events per room. Cancelling the call ends the wait.
- **`rooms://events/{resource}`**, a resource per room. `attach` returns its URI as
  `eventsResource`. Reading it, on either wire, lists the room's recent events from pages that agreed
  to the caller. Subscribing (`resources/subscribe`, which needs a session) gets
  `notifications/resources/updated` for it when such a page emits. On the stateless wire, poll it or
  use `next_request`. A client that checks the resources capability before subscribing needs the
  server built with `server.WithSubscriptions()`.

Neither starts a turn by itself: `resources/updated` wakes an agent that is already looping on a pull,
and `next_request` holds the agent's turn while it waits. For a push into a live session, use the
channel front end in [`experimental/integrations/roomsevents`](../../integrations/roomsevents).

## Calls

The bridge gives each call its own id on the relay, since an agent's request id is unique only in
its session. A page that leaves fails its pending calls at once; a page that does not answer fails
at `WithCallTimeout` (30s by default) and is sent `notifications/cancelled`. An agent's cancel
reaches the page's `Execute` as a cancelled context, and the page's progress reaches the agent under
the agent's own progress token.
