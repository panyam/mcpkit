# agent/web — Connect bridge over the agent host

`agent/web` exposes `agent/host.App` (the surface-agnostic host the terminal
`cmd/agentchat` drives) over a Connect RPC bridge with a live event stream, so a
browser can be another surface on the same running agent. It is a thin
projection: it holds only an `*App` and maps each RPC to an App method. No web or
proto type leaks back into `agent/host` (agent/CONSTRAINTS A4/A6).

This is E3 of the web-UI epic (issue 1196, epic 1193). The DockView + Solid
frontend is E4 (issue 1197); E3 ships a placeholder shell so the endpoints are
reachable and testable.

## Surface

Proto `mcpkit.agentweb.v1.HostService` (`protos/`, generated Go + Connect under
`gen/go/`):

| RPC | App method | Shape |
|-----|-----------|-------|
| `Watch` (server stream) | `App.Subscribe` | the retained event log replayed from offset 0, then live |
| `Submit` | `App.RunTurn` | run one turn |
| `Dispatch` | `App.Dispatch` | run a slash command → `CmdResult` |
| `RespondToAsk` | `App.RespondToAsk` | answer a pending elicitation (first responder wins) |
| `ListSessions` | `App.SessionsPage` | one page of persisted sessions |
| `GetStatus` | `App.ModelLabel` / `App.RunID` | status-line read |

### Frame envelope (A2)

`Watch` streams `Frame{kind, payload bytes}` where `payload = json.Marshal(HostEvent)`
— the event's own A2 wire JSON, a 1:1 projection, never a per-kind proto schema
(A2 forbids a translation layer). The client decodes `payload` as JSON keyed by
`kind`. `Dispatch` uses the same `{kind, json}` shape for `CmdResult`.

The first `Watch` frame is a ready sentinel with an empty `kind` (the Connect
protocol flushes response headers on the first `Send`, so this opens the stream
promptly even on an idle session). Clients skip an empty-kind frame.

A few `HostEvent` kinds still carry live pointers `json.Marshal` can reject
(`TaskStatus`, `Task`, and the `Failover` inside a command result) — that is the
already-filed issue 994. Until they get serializable snapshots, their frame
carries a minimal `{kind}` payload so one un-serializable event never stalls the
stream.

## Serve

`web.Handler(app)` builds the mux: HostService Connect handlers under the proto
service path, the frontend bundle under `/static/`, and the placeholder shell at
`/`. `cmd/agentweb` is the thin serve binary over it (mirrors how `cmd/agentchat`
is thin over the host):

```
go run ./cmd/agentweb --addr :8090 --config path/to/host-config.json
```

Connect server-streaming (`Watch`) rides the Connect protocol, which supports
server streams over HTTP/1.1, so no h2c is needed for local dev.

## Regenerating proto code

```
cd protos && buf generate
```
