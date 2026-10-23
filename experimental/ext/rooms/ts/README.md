# experimental/ext/rooms/ts

The page side of the room bridge (`experimental/ext/rooms`), for browsers. A page joins the relay
room for what it shows and offers tools there. An agent attached to the room through the bridge
calls them over MCP, and every call goes to one page. Pre-SEP and experimental, like the bridge. The
design is in issue [#1581](https://github.com/panyam/mcpkit/issues/1581).

It speaks to a [massrelay](https://github.com/panyam/massrelay) relay (v0.2.0 or later) and depends
on `@panyam/massrelay` (0.2.4 or later, whose `CollabClient` it joins through) and
`@panyam/servicekit-client` (0.1.9 or later).

```
npm install @panyam/mcpkit-rooms
```

It's published at mcpkit's release version, so `@panyam/mcpkit-rooms@X.Y.Z` is the page side of the
bridge that the Go modules carry at `vX.Y.Z`. Use the same version on both sides.

## Joining a room

```ts
import { joinToolRoom } from "@panyam/mcpkit-rooms";

const room = await joinToolRoom({
  relay: "https://relay.example.com",
  room: "design:demo/board.kicad_sch",
  token: relayToken, // minted by your app for its signed-in user
});
room.registerTool({
  name: "show_sheet",
  description: "Open a sheet of the design on screen",
  inputSchema: { type: "object", properties: { sheet: { type: "string" } }, required: ["sheet"] },
  async execute({ sheet }, { signal }) {
    await viewer.openSheet(String(sheet), { signal });
    return `showing ${sheet}`;
  },
});
room.setLabel("board.kicad_sch");
room.setAgentsAllowed(true); // "let my agents drive this tab", off by default
```

The tool shape is WebMCP's `registerTool` descriptor, so a tool written for WebMCP registers here
unchanged. `execute` may return a full MCP `CallToolResult`, a string, or any JSON value, and a
thrown error becomes an `isError` result. Its second argument carries:

- `signal`, aborted when the agent cancels or the page leaves. MCP sends no reply for a cancelled call.
- `progress(done, total?, message?)`, which reaches the agent under its own progress token.
- `from`, the bridge's peer id.

`annotations.readOnlyHint: true` lets an agent holding only `pages:read` call the tool. Anything
else needs `pages:act`. A tool may not declare an input property named `page`, which the bridge
adds so an agent can pick a page.

## Who may call

The relay validates `token` and reports its `sub` as the page's user. The bridge lets an agent call
the page only when the page agreed:

- `setAgentsAllowed(true)` agrees to every agent acting for the page's own user.
- `acceptPairing(code)` agrees to the agent that got `code` back from `attach`, typically another
  user's. The page then holds a grant for that agent's principal (subject, client), sends it to
  every bridge, and `onPaired(fn)` reports it. `onPairingRequest(fn)` reports who is asking.
- `offerPairingCode({ ttlMs? })` makes up a code for the page to show; the user gives it to the
  agent, which enters it with the bridge's `pair` tool. `onPaired(fn)` reports which agent it was,
  and the code is withdrawn once used. Two minutes by default, at most ten.
- Each call's `ctx.caller` is the agent's `{ subject, client? }` from the bridge's validated token,
  so a tool can refuse per caller. Any peer in the room can send the page a call, though, so don't
  rely on it alone yet (the Go page has `TrustBridges`; the TS one waits on massrelay passing peers'
  relay identity).
- `revoke({ subject, client? })` ends grants to that principal, `revokeAll()` ends every grant, and
  `grants()` lists them. A grant outlives the agent's MCP session and ends when revoked or when the
  page leaves.

## Telling agents what happened

```ts
room.ask("why is SDA flagged?");           // room.ask
room.selectionChanged({ net: "SDA" });      // page.selection_changed
room.emit("viewer.sheet_opened", { sheet }); // a name of your own
```

Agents subscribed to the room's events hear these, if this page agreed to their session (the same
agreement as calls). Names are one to four dot-separated lowercase segments (`validEventName`), and
`data` is capped at `MAX_EVENT_DATA_BYTES` (16 KiB). `emit` throws on either before anything is sent.
An event goes only to bridges that have already listed the page, so one emitted straight after
`joinToolRoom` may reach nobody. Agents whose host speaks no events hear the same events through the
bridge's `next_request` tool or its `rooms://events/{resource}` resource, when the bridge enables
them (`rooms.WithEventFallbacks`).

## The same tool in an MCP Apps frame

`registerOnApp(app, tool)` registers a tool with an MCP Apps bridge, such as mcpkit's
`window.MCPApp` from ext/ui, so one tool object serves a host that frames the app and a room an agent
attaches to:

```ts
registerOnApp(window.MCPApp, showSheet);
(await joinToolRoom({ relay, room, token })).registerTool(showSheet);
```

`modelContext: true` also registers each tool with the browser's own WebMCP context
(`navigator.modelContext`, then `document.modelContext`), when one is there.

## Developing

```
pnpm install
pnpm check    # tsc
pnpm test     # needs Go: the tests start the Go bridge on a real relay
```

`make test-experimental-rooms-ts` runs the same from the repo root, and CI runs it in the `test-rooms` job.
