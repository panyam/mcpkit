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

servicekit v0.1.7's `grpcws.BidiStreamConn.OnClose` reads a message counter that its forwarding
goroutine updates with `atomic.AddInt64`, so every WebSocket close inside the relay is a reported
race. It is in the relay, not here: no race report across five runs had a frame in this module.
`experimental/scripts/test-rooms-massrelay.sh` runs everything without `-race`, then the embedded
transport subtests, which open no WebSocket, with it.

## Adding the module converged the tree's dependencies

massrelay v0.2.1 requires newer x/net, x/crypto, x/sys, x/text, otel, genproto and servicekit
than the rest of the tree pinned, and `scripts/dep-baseline.json` was empty. Rather than accept 13
divergences there, the PR ran `check_dep_consistency.py --unify` and `make tidy-all`, which raised
every module to those versions. That is most of the PR's go.mod and go.sum churn.
