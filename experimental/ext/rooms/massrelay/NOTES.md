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
page's MCP handler without addressing it, so `handle` drops any `AppMessage` whose `to_client_id`
is not this peer.

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

## Not every test runs under -race

servicekit's (v0.1.7, still in v0.1.9) `grpcws.BidiStreamConn.OnClose` reads a message counter that its forwarding
goroutine updates with `atomic.AddInt64`, so every WebSocket close inside the relay is a reported
race. It is in the relay, not here: no race report across five runs had a frame in this module.
`experimental/scripts/test-rooms-massrelay.sh` runs everything without `-race`, then the embedded
transport subtests, which open no WebSocket, with it.

## Adding the module converged the tree's dependencies

massrelay v0.2.1 requires newer x/net, x/crypto, x/sys, x/text, otel, genproto and servicekit
than the rest of the tree pinned, and `scripts/dep-baseline.json` was empty. Rather than accept 13
divergences there, the PR ran `check_dep_consistency.py --unify` and `make tidy-all`, which raised
every module to those versions. That is most of the PR's go.mod and go.sum churn.

Moving to massrelay v0.2.4 did the same for servicekit, v0.1.7 to v0.1.9 in all 75 modules,
the root included since it requires servicekit directly. No source changed. servicekit's
`BidiStreamConn.OnClose` race inside the relay is still there in v0.1.9, so this module's WebSocket
tests still run without `-race`.

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
