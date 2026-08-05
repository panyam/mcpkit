# agent/web — Connect bridge over the agent host

`agent/web` exposes `agent/host.App` (the surface-agnostic host the terminal
`cmd/agentchat` drives) over a Connect RPC bridge with a live event stream, so a
browser can be another surface on the same running agent. It is a thin
projection: it holds only an `*App` and maps each RPC to an App method. No web or
proto type leaks back into `agent/host` (agent/CONSTRAINTS A4/A6).

This is E3 of the web-UI epic (issue 1196, epic 1193). E4 (issue 1197) adds the
DockView + Solid frontend that consumes this bridge — see "Frontend" below.

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
service path, the frontend bundle under `/static/`, and the server-rendered shell
at `/` (`shell.go`, stdlib `html/template`, island holes + `data-layout`).
`cmd/agentweb` is the thin serve binary over it (mirrors how `cmd/agentchat` is
thin over the host):

```
go run ./cmd/agentweb --addr :8090 --config path/to/host-config.json
```

Connect server-streaming (`Watch`) rides the Connect protocol, which supports
server streams over HTTP/1.1, so no h2c is needed for local dev.

## Frontend (E4, issue 1197)

The frontend lives in `web/` and follows the diffpp/Agni pattern: a server-rendered
shell with island "holes", DockView for the dockable desktop layout, SolidJS
islands mounted into the holes, and Connect-Web clients generated from the same
proto. It consumes E3's `Watch` server-stream **directly** via connect-web (no
massrelay — that is a later follow-up). This slice ships one **Conversation** panel
(streams the live turn off `Watch`, a prompt box that `Submit`s, an approval prompt
on `HostElicitRequest` answered via `RespondToAsk`) in both a DockView desktop
layout and a mobile-overlay layout; the observability panels are E5 (issue 1198),
which extend the panel registry in `web/src/dock.ts` and reconcile into existing
saved layouts.

Layout map (`web/src/`):

| File | Role |
|------|------|
| `api.ts` | connect-web `HostService` client (same-origin transport) |
| `watch.ts` | `Watch` subscription: decode each `Frame` payload → `HostEvent`, reconnecting |
| `hostevent.ts` | wire types mirroring `agent/host/hostevent.go` |
| `conversation.ts` | shared store: folds `HostEvent`s, drives `Submit` / `RespondToAsk` |
| `ConversationPanel.tsx` | the Conversation Solid island (transcript, ask prompt, compose box) |
| `dock.ts` | framework-neutral panel registry + saved-layout persistence + reconcile |
| `DockviewWorkspace.ts` | dockview-core glue: adopt island hosts into panels |
| `MobileOverlays.tsx` | mobile layout: the same Conversation panel as an overlay |
| `main.ts` | boot: one store, start `Watch`, mount the layout the shell selected |

### Run it (offline demo)

```
./run.sh          # builds the bundle, serves --demo on :8090
```

`--demo` wires an offline, inexhaustible streaming provider (no model or MCP
server needed), so opening `http://localhost:8090/` and sending a message streams a
live turn into the Conversation panel. `?layout=mobile` (or the topbar Layout
select) switches to the mobile-overlay surface.

### Build / test the frontend

```
cd web
npm install
npm run gen      # regenerate TS clients from ../protos (buf)
npm run build    # esbuild bundle → ../static (committed; Go embeds it)
npm run typecheck
npm run test     # vitest: Frame-decode, event fold, dock reconcile
```

The built bundle is committed under `static/` so `go build` and `just test-agent`
need no Node step; rebuild it after a frontend change.

## Regenerating proto code

```
cd protos && buf generate          # Go + Connect (gen/go)
cd web && npm run gen              # TypeScript clients (web/src/gen)
```
