# ext/rooms/massrelay — implementation notes

## Speaking the WebSocket directly

massrelay ships a TypeScript client and no Go one, so `Dial` speaks the wire itself: servicekit's
grpcws envelope (`{type, data, error, pingId}`) over gorilla/websocket, with `data` a protojson
`CollabAction` going out and a `CollabEvent` coming in. The relay sends `ping` envelopes, not
WebSocket pings, and expects a `pong` with the same `pingId`; the read loop answers them. The
relay stamps a message's sender from the connection and ignores the client id in the action, so
`Dial` leaves it empty.

## Only addressed messages are reported

`RoomTransport` promises the messages addressed to this peer. massrelay also fans out room-wide
`AppMessage`s, which other apps in the room may use. Reporting those would let any peer reach a
page's MCP handler without addressing it, so `handle` drops any `AppMessage` whose
`to_client_ids` doesn't hold this peer. Since v0.3.0 the relay rewrites that list to just the
receiver's id, so a receiver never learns who else a message went to.

## SendEach and the relay's PEER_NOT_FOUND

`SendEach` checks targets against the roster and sends one `AppMessage` listing the ones it
knows. A peer that left in the gap comes back from the relay as one `PEER_NOT_FOUND` whose
message names every missing id ("Not in this room: a, b"), and the relay still delivers to the
rest. Over the embedded transport that answer is synchronous, and `missingPeers` reads the ids
out of the text, since v0.3 has no structured field for them. Over `Dial` it arrives as an
`ErrorEvent` on the stream with nothing tying it to the send, so a dialled `SendEach` reports
only what the roster knew.

## A leave does not close the relay's channel

For a non-owner, massrelay's `handleLeave` removes the client and never closes its `SendCh`, so the
embedded reader stops on the member's own `done` channel rather than waiting for a close. Joining
as owner would be worse: the owner leaving ends the session for everyone, which is why both modes
join as ordinary peers.

The relay drops events once a client's 64-slot channel is full. The embedded reader drains into the
member's 1024-slot buffer straight away, and a member that falls that far behind is disconnected,
as in memroom, rather than left missing messages.

## Tests run a real relay

The tests start massrelay's HTTP app with JWT auth required (an HS256 key in oneauth's in-memory
key store) and its per-IP rate limit off, since every test peer dials from loopback. The bridge
joins embedded and pages dial in, so the acceptance cases cross the WebSocket path and the
in-process path in one run.

## Every test runs under -race, since servicekit v0.1.10

Through servicekit v0.1.9, `grpcws.BidiStreamConn.OnClose` read a message counter that its
forwarding goroutine updated with `atomic.AddInt64`, so every WebSocket close inside the relay was a
reported race, with no frame in this module. `experimental/scripts/test-rooms-massrelay.sh` ran the
WebSocket tests without `-race` and only the embedded subtests with it. servicekit v0.1.10 made the
counters `atomic.Int64` (servicekit issue 41), and the script now runs the whole module under
`-race`.

## Adding the module converged the tree's dependencies

massrelay v0.2.1 requires newer x/net, x/crypto, x/sys, x/text, otel, genproto and servicekit
than the rest of the tree pinned, and `scripts/dep-baseline.json` was empty. Rather than accept 13
divergences there, the PR ran `check_dep_consistency.py --unify` and `make tidy-all`, which raised
every module to those versions. That is most of the PR's go.mod and go.sum churn.

Moving to massrelay v0.2.4 did the same for servicekit, v0.1.7 to v0.1.9 in all 75 modules,
the root included since it requires servicekit directly. No source changed. massrelay v0.2.5 moved
it again, to servicekit v0.1.11, whose v0.1.10 fixed the `OnClose` race above.

Moving to massrelay v0.2.3 did the same for oneauth, v0.2.2 to v0.3.0 across the ten modules that
use it, which was also wanted in its own right. Nothing needed a source change, and the Keycloak
interop suite (`make testkcl-auto`) passed 19 of 19 on it. That suite runs in no workflow, though,
and had been failing one test on `main` since #1567 for an unrelated reason: a raw stateless request
without routing headers.

## A test stack for the TypeScript client

`internal/relaytest` starts the relay the Go tests use, and `internal/cmd/teststack` wraps it with
the bridge and an MCP endpoint, printing their URLs and tokens as a JSON line. It serves until its
stdin closes. The page client's tests in `../ts` run it with `go run`, so they test the TS page
against the real Go bridge rather than a fake one.

## Sending again what the relay drops for the rate (#1621)

The relay answers an action over its per-connection rate with `ErrorEvent{RATE_LIMITED}`, whose
message names the action's id (`Dropped action "d12": over 30 messages/sec`), and drops it before
any peer sees it. That makes a resend safe, since it can't arrive twice. Nothing here set an action
id before this, so the message named `""` and there was no telling which reply had gone. Now the
dialled transport stamps `d1`, `d2`, … and keeps the last 256 actions (`resend.go`), and the TS
page does the same with `p1`, `p2`, … (`../ts/src/sender.ts`).

A dropped action goes again after one second, then two, then four, and is given up on with a log
line after that. The relay's limiter refills every second, so a shorter first wait mostly gets
refused again. A resend doesn't keep its place in the order, so a reply can overtake the
notifications sent before it; JSON-RPC replies carry ids, and nothing in the page protocol depends
on that order across a drop.

Pacing (`WithSendRate`, the page's `sendRate`) keeps a connection under the limit in the first
place, so the resend is for a relay configured below 25 a second, or a page whose pacing is off.
The message text is the only place the id appears, so `droppedAction` parses it, the way
`missingPeers` parses `PEER_NOT_FOUND`. A relay that changes the wording breaks both quietly,
which `TestDroppedAction_ReadsTheRelaysMessage` and the burst tests would catch.
