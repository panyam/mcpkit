# ext/rooms/ts — implementation notes

## Built on GRPCWSClient, not massrelay's CollabClient

massrelay's TypeScript `CollabClient` (0.2.1) can't be the transport here, for two reasons. It
builds the socket URL itself as `${relay}/ws/v1/${sessionId}/sync`, so there is nowhere to put the
`?token=` a browser WebSocket needs to authenticate, and it does not escape the session id. A room
named `design:demo/board.kicad_sch` puts its `/` into the path, and the relay's `{session_id}`
route never matches. The page client therefore uses servicekit's `GRPCWSClient`, which
`CollabClient` sits on, and massrelay's generated `./models` schemas, which is the same wire with
the URL under our control. The Go adapter (`../massrelay`) speaks the same wire, for the same lack
of a client.

`CollabClient` also drops `to_client_id` from what `onAppMessage` reports, and the page has to see
it to ignore room-wide messages.

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
