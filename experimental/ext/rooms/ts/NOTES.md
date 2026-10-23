# ext/rooms/ts — implementation notes

## Built on massrelay's CollabClient, which took three releases

The page client joins through massrelay's `CollabClient`. Getting there took three massrelay
releases, and the history explains the shape of `room.ts`.

- **0.2.1** (slice 2 shipped on it). `CollabClient` couldn't be used at all: it had no token
  option, it didn't escape the room in the URL (so `design:demo/board.kicad_sch` broke the route),
  and `onAppMessage` dropped `to_client_id`. The page spoke the wire itself through servicekit's
  `GRPCWSClient`.
- **0.2.3** fixed those three (massrelay#31). The move was tried and every existing test passed,
  but `CollabClient` still treated every relay `ErrorEvent` as fatal and closed without calling
  `onDisconnect`. The relay answers a message addressed to a peer that has left with
  `PEER_NOT_FOUND`. That happens whenever an agent disconnects while a page tool is still running,
  so on 0.2.3 the page would have dropped out of the room unnoticed. The test "stays in the room
  when it answers a caller that already left" was added to pin it, and it went red on that version.
- **0.2.4** keeps the stream open after a post-join error (massrelay#33), reports it through
  `onErrorEvent`, and pairs every `onConnect` with one `onDisconnect`, so the page moved over.

So `onErrorEvent` means refusal only before the join. After it, the page ignores the event and
stays. `closed` resolves from `onDisconnect`, and `maxRetries` is pinned to 0, because a reconnect
would rejoin under a new peer id that no pairing knows.

`@panyam/servicekit-client` stays a direct dependency. `CollabClient` accepts a WebSocket
implementation only through its `_grpcFactory` hook, so `joinToolRoom`'s `WebSocket` option builds a
`GRPCWSClient` for it. The tests' raw peers use it as well. The Go adapter (`../massrelay`) still
speaks the wire itself, since massrelay has no Go client.

## Only addressed messages are handled

As in the Go transport, an `AppMessage` that is not addressed to this page is dropped, whatever its
kind. Otherwise any peer in the room could run a page tool by broadcasting a `tools/call` without
going through the bridge's pairing. The interop test sends exactly that and checks nothing ran.

## The tests run the real Go bridge

`test/stack.ts` starts `rooms/massrelay/internal/cmd/teststack` with `go run`. That is the Go room
bridge joined embedded to a massrelay relay that requires JWTs, and it prints the URLs and tokens as
one JSON line. The agent in the tests is the official TypeScript MCP client, so every hop crosses
the language boundary: TS agent, Go bridge, relay, TS page. The first test file pays for compiling
the Go side.

The ext/ui frame case loads ext/ui's committed `mcp-app-bridge.js` into a `JSDOM` window of its
own, rather than running the file under vitest's jsdom environment. Under that environment the
global `Event` is jsdom's while `WebSocket` is Node's undici, and undici rejects the foreign event
on open. Path resolution reads `import.meta.url` as a string for a similar reason: jsdom's `URL` is
not the one `fileURLToPath` accepts.

## The release-age exemption

pnpm 12 refuses packages published within the last day (`minimumReleaseAge`). This package bumps
`@panyam/massrelay` right after a massrelay release, which would fail every install for a day, so
`pnpm-workspace.yaml` exempts that one package. Everything else keeps the policy.
