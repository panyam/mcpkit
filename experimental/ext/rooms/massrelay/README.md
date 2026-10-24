# experimental/ext/rooms/massrelay

`rooms.RoomTransport` over [massrelay](https://github.com/panyam/massrelay), so the room bridge
(`experimental/ext/rooms`) and its pages meet in a real relay. Its own module, so neither mcpkit nor
`experimental/ext/rooms` depends on massrelay. Needs massrelay v0.2.0 or later, for addressed
`AppMessage`s and `PeerInfo.identity`.

```
go get github.com/panyam/mcpkit/experimental/ext/rooms/massrelay
```

## Embedded: the relay runs in your process

When one server hosts the relay and the bridge, join through the `CollabService` directly:

```go
app := server.NewRelayApp()          // github.com/panyam/massrelay/web/server
_ = app.Init()
bridge := rooms.New(massrelay.Embedded(app.Service, &services.Principal{Subject: "bridge"}))
```

The `Principal` is the identity the relay records for the peer, exactly as it would from a
validated token, so pass one the host authenticated. Its `Rooms` limit which rooms it may join, as
the token's `rooms` claim does.

## Dial: the relay runs elsewhere

```go
t := massrelay.Dial("https://relay.example.com", relayToken)
page, err := rooms.JoinPage(ctx, t, "design:demo/board.kicad_sch")
```

Each `Join` opens one WebSocket to `<base>/ws/v1/<room>/sync`, carrying the token as a Bearer header.
The relay validates it and reports its `sub` as the peer's `Subject`, which is what the bridge
compares against the agent's subject. A bad token fails the join with the relay's 401, and a room
outside the token's `rooms` claim fails it with `ROOM_FORBIDDEN`. `WithDialer` sets TLS or proxy
options and `WithUsername` the name other peers see.

## What maps to what

| rooms | massrelay |
|-------|-----------|
| a room | a session whose id is the resource name |
| `Peer.ID` | the relay's client id |
| `Peer.Subject` | `PeerInfo.identity.subject`, from the validated token |
| `Send(to, kind, payload)` | an `AppMessage` with `to_client_ids: [to]` |
| `SendEach(to, kind, payload)` | one `AppMessage` listing every target (massrelay v0.3.0 and later) |
| `PeerJoined` / `PeerLeft` | `PeerJoined` / `PeerLeft` |
| `Events` closes | `Leave`, the connection dropping, or `SessionEnded` |

Room-wide `AppMessage`s (empty `to_client_ids`) and the relay's collaboration traffic (presence, scene
updates) are not reported, since the bridge sends neither.

## Limits the relay sets

- 10 peers per room by default (`CollabService.MaxPeersPerRoom`): the bridge plus nine pages.
  A full room fails the join with `ROOM_FULL`.
- 30 messages a second per WebSocket connection by default, past which the relay drops messages
  without telling the sender. `Dial` paces each connection at 25 a second (`WithSendRate`, 0 turns
  it off), so a bridge's fan-out waits instead of losing messages. `Notify` sends one message
  per room whatever the number of tabs, so it rarely gets near the limit. The embedded
  transport doesn't cross that limit. The TypeScript page doesn't pace yet, so a page answering a
  burst of calls can still hit it.
- 1 MB per message.
