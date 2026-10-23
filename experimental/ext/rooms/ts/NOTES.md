# ext/rooms/ts — implementation notes

## Built on GRPCWSClient, not massrelay's CollabClient

The page client talks to the relay through servicekit's `GRPCWSClient`, the socket `CollabClient`
sits on, plus massrelay's generated model schemas. It's the same wire, with the connection's
lifetime under our control. The Go adapter (`../massrelay`) speaks the same wire, since massrelay
has no Go client.

Slice 2 shipped on 0.2.1, where `CollabClient` couldn't be used at all: it had no token option, it
didn't escape the room in the URL, and its `onAppMessage` dropped `to_client_id`. massrelay 0.2.3
fixed all three (massrelay#31). The move onto it was then tried, and every existing test passed. One
thing still rules it out. `CollabClient` treats every relay `ErrorEvent` as fatal: it closes the
socket and resets its state before the close lands, so `onDisconnect` never fires. The relay answers
a message addressed to a peer that has left with `ErrorEvent{PEER_NOT_FOUND}`. That happens whenever
an agent disconnects while a page tool is still running and the page then sends its result. On
`CollabClient` the page would drop out of the room at that point, and its `closed` promise would
never resolve. The test "stays in the room when it answers a caller that already left" pins it, and
it goes red on the `CollabClient` version. The move can happen once `CollabClient` treats
`PEER_NOT_FOUND` as refusing one message rather than ending the connection.

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
