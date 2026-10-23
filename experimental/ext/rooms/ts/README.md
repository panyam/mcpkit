# experimental/ext/rooms/ts

The page side of the room bridge (`experimental/ext/rooms`), for browsers. A page joins the relay
room for what it shows and offers tools there. An agent attached to the room through the bridge
calls them over MCP, and every call goes to one page. Pre-SEP and experimental, like the bridge. The
design is in issue [#1581](https://github.com/panyam/mcpkit/issues/1581).

It speaks to a [massrelay](https://github.com/panyam/massrelay) relay (v0.2.0 or later) and depends
on `@panyam/massrelay` and `@panyam/servicekit-client`. It isn't on npm yet. `package.json` points at
the TypeScript source (`src/index.ts`), so a bundler can take it from a checkout or a git dependency.

## Joining a room

```ts
import { joinToolRoom } from "@mcpkit/rooms-page";

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

The relay validates `token` and reports its `sub` as the page's user. The bridge lets a session call
the page only when the page agreed:

- `setAgentsAllowed(true)` agrees to every agent session acting for the page's own user.
- `acceptPairing(code)` agrees to the session that got `code` back from `attach`, typically another
  user's. `onPairingRequest(fn)` reports who is asking, so the page can show its user.
- `revoke(session?)` ends one pairing, or all of them when called with no argument.

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
