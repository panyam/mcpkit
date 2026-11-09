# The page wire, `mcp/1`

What a page and a room bridge send each other. A page client or a bridge written in another language
needs this page and nothing else. The Go implementation is `experimental/ext/rooms`
(`Page`, `Bridge`), and the TypeScript one is `@panyam/mcpkit-rooms`. Both replay
[`testdata/wire/page.jsonl`](testdata/wire/page.jsonl), which is the executable form of this page.
The design is in issue #1581.

Three other contracts sit around this one, and each is written down where it lives:

- MCP itself, between agents and the bridge.
- The relay's own frames, in massrelay's protobuf. They carry this wire.
- MCP Events (SEP-3415), which roomsevents speaks to agents.

## Frames

A page and a bridge are peers in one relay room. The room is named after the resource the page shows,
such as `design:demo/board.kicad_sch`. Every message between them is one relay `AppMessage`:

- `kind` is `mcp/1`.
- `payload` is one JSON-RPC 2.0 message as UTF-8 JSON.
- It is addressed to exactly one peer (`to_client_ids` with one id). Neither side broadcasts.

A receiver ignores frames of any other kind, and unknown fields in a message. The relay stamps each
frame with the sender's client id and the subject from its validated token. Both sides read identity
only from there, and never from a payload.

## Who talks to whom

The bridge is the MCP client and the page is the MCP server. The bridge sends requests, and the page
answers them. Either side sends notifications.

- **No `initialize`.** The relay already authenticated both peers, and this page fixes the page's
  capabilities. The bridge's first message to a page is `tools/list`.
- **A page cannot address a bridge it has not heard from.** It learns a bridge's id from the bridge's
  first request, and sends page-to-bridge notifications only to bridges it has heard from. Each
  page-to-bridge notification below goes to every such bridge.
- **`trustBridges`.** A page that knows its host's bridge subjects answers only peers whose relay
  subject is one of them. A page that doesn't know them answers any peer in the room, so any holder of
  a relay token for the room can call it and claim any caller. The join response names the subjects.

## Requests, bridge to page

| Method | Params | Result |
|---|---|---|
| `tools/list` | none | `{tools: [Tool]}`, MCP's `Tool` shape. `annotations.readOnlyHint` is always present. |
| `tools/call` | MCP's, plus `_meta["io.mcpkit/rooms/caller"]: {subject, client?}` | MCP's `CallToolResult` |
| `events/list` | none | `{events: [EventType]}`, SEP-3415's shape (below) |

`_meta["io.mcpkit/rooms/caller"]` is the agent the bridge is calling for, from the bridge's validated
MCP token. A page can refuse per caller. It is only as trustworthy as the frame's sender, which is
why `trustBridges` exists.

A `tools/call` may carry `_meta.progressToken`. The page then sends `notifications/progress` with
that token, as MCP describes. The bridge cancels with `notifications/cancelled {requestId}`, and the
page sends no response for a cancelled call.

Errors:

- A `tools/call` naming a tool the page doesn't offer: `-32602`, message `unknown tool: <name>`.
- A tool that fails: a result with `isError: true` and the error as text, not a JSON-RPC error.
- Any other method: `-32601`. A page from before `events/list` answers that, and the bridge treats
  it as a page that declares no events.

## Notifications, page to bridge

| Method | Params | Meaning |
|---|---|---|
| `notifications/tools/list_changed` | none | The page's tools changed. The bridge lists them again. |
| `notifications/events/list_changed` | none | The page's declared event types changed. |
| `notifications/rooms/page_state` | `{label, focused?, focusedAt?}` | `label` is what `list_pages` shows. `focused: true` stamps the page's last focus, at `focusedAt` (RFC 3339) when given. |
| `notifications/rooms/agents_allowed` | `{allowed}` | The "let my agents drive this tab" switch. Off by default. |
| `notifications/rooms/grants` | `{grants: [{subject, client?}]}` | Everyone the page agrees to beyond the switch, as the full list each time. |
| `notifications/rooms/accept_pairing` | `{code}` | The page's user accepted an agent's code, upper-cased. |
| `notifications/rooms/pairing_offer` | `{code, ttlMs}` | A code the page shows its user, for an agent's `pair` tool. An empty code withdraws it. Bridges honour at most 10 minutes. |
| `notifications/rooms/event` | `{name, data}` | Something happened on the page (below). |

## Notifications, bridge to page

| Method | Params | Meaning |
|---|---|---|
| `notifications/rooms/pairing_requested` | `{subject, client?, sameSubject}` | An agent attached to the room and may not call the page yet. With `sameSubject` true the switch is enough. Otherwise the user needs a code. |
| `notifications/rooms/paired_by_code` | `{subject, client?, sameSubject, code}` | A principal paired by a code: one the page offered, or one it accepted. |
| `notifications/rooms/presence` | `{agents: [{subject, client?, lastSeen, acting}]}` | The agents this bridge has seen around the page. Each bridge reports its own, and the page merges them by principal. |
| `notifications/rooms/notify` | `{name, data}` | A message from the host, not on any agent's behalf. Any peer can send one, so it is a prompt to refresh, not the change itself. |
| `notifications/rooms/tool_refused` | `{name, reason}` | The bridge won't offer one of the page's tools. |
| `notifications/rooms/event_refused` | `{name, reason}` | The bridge didn't accept an event declaration, or dropped an event emitted under it. |

None of these gets an answer.

## Greeting

The first time a page hears from a bridge, it sends that bridge the state it would otherwise have
missed, before answering:

1. `page_state`: with `focused` and `focusedAt` if the user ever focused the page, else the label
   alone, if set.
2. `agents_allowed {allowed: true}`, if the switch is on.
3. `grants`, if there are any.
4. `pairing_offer`, if a code is on offer, with the time it has left.

Both pages send them in this order. A bridge must not rely on the order, and applies each message on
its own. A bridge handles one peer's messages in order, so by the time it reads the `tools/list`
answer it has also read the greeting.

## Agreement and pairing

An agent may call a page only when the page agreed to it. The page holds the agreement and the bridge
enforces it.

- **The switch.** `agents_allowed` agrees to every agent whose subject is the page's own user.
- **An agent's code.** `attach` gives the agent a code, which it shows its user. The page's user
  types it in. The page sends `accept_pairing`, and the bridge that minted the code answers with
  `paired_by_code`. The page adds the grant and sends `grants` again to every bridge.
- **The page's code.** The page sends `pairing_offer`. An agent enters the code with `pair`, and the
  bridge sends `paired_by_code`. The page withdraws the offer (`pairing_offer {code: ""}`), adds the
  grant, and sends `grants`.

A page honours `paired_by_code` only for a code it offered or accepted that has not expired, so a
peer that is not a bridge cannot grant itself anything.

## Page events

`notifications/rooms/event {name, data}` reports something that happened on the page. `name` is one
to four dot-separated lowercase segments, each starting with a letter, at most 64 characters
(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$`). `data` is any JSON, at most 16 KiB encoded. A bridge
drops a malformed event silently. Two names are built in:

- `room.ask` with `{text}`: the page's user asked the agents something.
- `page.selection_changed` with `{selection}`.

A page may declare its other names. It answers `events/list` with SEP-3415 event types:

```json
{"name": "item.found", "description": "The player found an item.",
 "payloadSchema": {"type": "object", "properties": {"item": {"type": "string"}}},
 "_meta": {"io.github.panyam.mcpkit/context-hint": {"priority": "high", "template": "found {{item}}"}}}
```

- Declared `data` is a JSON object, and `payloadSchema` must have `type: "object"`.
- The schema may not declare `resource`, `page`, `user`, `label` or `at`. Agents receive those beside
  the page's own fields.
- The context hint is opaque to the bridge. Hosts read it.
- The bridge asks for `events/list` after listing the page, and again on
  `notifications/events/list_changed`. Until it has the answer, it treats a declared name as
  undeclared.
- The bridge checks each event emitted under a declared name against the schema. It drops one that
  doesn't match and sends `event_refused` with the reason.
- A bridge with a fixed catalog also refuses an undeclared name, apart from the two built-in ones.

## Joining a room

A page needs a relay URL, a room, and a relay token admitting it to that room. A host that serves the
join endpoint (`rooms.JoinHandler`) answers a POST, made with the page's own credentials, with:

```json
{"room": "design:demo/board.kicad_sch", "relayUrl": "/relay", "token": "eyJ…",
 "bridgeSubjects": ["rooms-bridge"], "expiresAt": "2026-11-09T07:58:41Z"}
```

- `relayUrl` may be relative to the endpoint.
- `bridgeSubjects` is the page's `trustBridges`.
- The endpoint answers 401 when the caller isn't signed in, 403 when it may not open the room, and
  405 for any method but POST. Every answer is marked `no-store`.

The token is a JWT the relay validates. It carries `sub` (the page's user, which pairing compares),
`client_id` (the relay finds the signing key by it), `iss`, `type: "access"`, `iat`, `exp` and
`rooms`. `rooms` lists the rooms the token may join, each a room name or a prefix ending in `*`. A
join endpoint names exactly one. The relay refuses a join to any other room with `ROOM_FORBIDDEN`. It
ends a connection at the token's `exp`, unless the page sends a fresh token over the open socket
(massrelay's `ReAuth`) first, which keeps its client id. A page re-joins only by getting a new id,
and every pairing names the old one, so it refreshes rather than reconnects.

## Versioning

`mcp/1` names this wire. Adding a method, a notification, or an optional field is compatible,
because receivers answer unknown requests with `-32601`, ignore unknown notifications, and ignore
unknown fields. Each such change goes into this page and the fixture together. Anything else,
such as changing a field's meaning, removing a method, or requiring a new field, needs a new kind,
`mcp/2`. A bridge and a page then each speak the kinds they know, and a receiver ignores frames of a
kind it doesn't speak. A second version field would duplicate the kind, so there is none.

## The fixture

`testdata/wire/page.jsonl` holds one step per line:

- `{"page": "<action>", ...}`: the page's own API, such as `register_tool`, `declare_event`,
  `set_label`, `set_agents_allowed`, `emit` or `accept_pairing`.
- `{"bridge": {...}}`: a frame the bridge sends the page.
- `{"page_sends": {...}}`: the next frame the page must send. It matches when every field it names is
  present with that value. Arrays match element by element, and `"$ANY"` matches any value present.
- `{"page_sends_nothing": true}`: the page sends nothing for a short while.
- `{"note": "..."}`: commentary.

The fixture's `show_sheet` tool answers the text `showing <sheet>`. Go replays it in
`wire_fixture_test.go` and TypeScript in `ts/test/wire.test.ts`. Both drive a real page from a bare
relay peer that plays the bridge. A change to the wire changes this page, the fixture and both pages
together, or one of the replays fails.

## Later: the tool surface follows WebMCP

Not part of the wire's contract yet. #1679 records the plan. The tool half of this wire takes
WebMCP's `registerTool` descriptor, and the MCP Apps draft maps that descriptor to MCP in
ext-apps#829. Rooms differs from that mapping today in three places:

- Results: a string becomes text content, and other values become JSON text plus
  `structuredContent`. #829 sends `content: []` and wraps non-objects as `{result: v}`.
- Rooms forwards every annotation, where #829 carries only `readOnlyHint`.
- Rooms looks for `navigator.modelContext` before `document.modelContext`.

Each will be aligned or listed here as a deliberate divergence.
