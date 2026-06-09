# kitchen-sink — per-subscription delivery showcase

Single-process reference for the **per-subscription delivery surface** the events η work shipped: `Match` / `Transform` / `OnSubscribe` / `OnUnsubscribe` hooks on `EventDef`, plus `EmitToSubscription` for targeted delivery. Three event sources, multiple subscribers per source with different params, so the per-subscription model is visible on the wire instead of being theoretical.

Companion to [`whole-enchilada/`](../whole-enchilada/) (deploy axis) and [`discord/`](../discord/) / [`telegram/`](../telegram/) (real-upstream single-source demos).

## Quick run

```bash
make serve            # terminal 1 — start the server (port 8080)
make demo             # terminal 2 — interactive walkthrough
make demo-test        # OR: non-interactive (for CI / scripting)
make readme           # regenerate WALKTHROUGH.md
```

## Tracing (optional, SEP-414 P6 — issue 667)

The server is wired to opt into OpenTelemetry trace emission via the `--exporter` flag (or `EXPORTER` env var). Four values, matching every other mcpkit example — see [`examples/CONVENTIONS.md`](../../CONVENTIONS.md) § Telemetry wiring for the full matrix.

```bash
# Default — Noop TracerProvider, zero overhead, no spans.
make serve

# Print every span to stdout (teaching / debugging mode).
EXPORTER=stdout make serve

# Hand spans to the docker/observability LGTM stack.
# Bring it up first: (cd ../../../docker/observability && docker compose up -d)
# Then either:
make serve-otlp                          # equivalent to EXPORTER=auto — silent fallback if stack isn't up
EXPORTER=otlp make serve                 # warns on dial failure
```

When the LGTM stack is up, open Grafana → Tempo → service `kitchen-sink-events`. Spans you'll see:

- **One dispatch span per JSON-RPC method** (subscribe / unsubscribe / poll / list / etc.) — from the SEP-414 P2 server middleware.
- **`events.webhook.deliver` spans** around each outbound webhook POST (when a webhook subscriber is registered) — from `events.WithWebhookTracerProvider`.

Per-subscription `Match` / `Transform` spans on the fanout hot path are NOT emitted today — that's a hot-path concern (fanout runs every 2s × N subscribers) needing sampling design. Tracked as a follow-up; see [issue 667](https://github.com/panyam/mcpkit/issues/667) for the design options.

## What it demonstrates

Each bullet maps to a walkthrough step:

- **events/list** — three sources advertised: `chat.message` (cursored), `alert.fired` (cursored), `presence.changed` (cursorless).
- **`Match` by params** — two subs to `chat.message` with `params:{channel:"general"}` vs `{channel:"dev"}`. Each sees only its channel; the unrelated `alerts` channel reaches neither.
- **`Transform` by params** — two subs to `alert.fired` with `params:{redact_pii:true}` vs `{redact_pii:false}`. Same upstream event, different bytes per sub: the redacted variant has its `reporter` cleared and emails in the `message` body replaced with `<redacted-email>`.
- **`OnSubscribe` + `EmitToSubscription`** — `presence.changed` doesn't use Match; instead, `OnSubscribe` captures `params:{watch_users:[...]}` in a per-sub registry and the presence feeder uses `events.EmitToSubscription(idx, ev, subID)` to deliver each transition straight to the matching subs. Match and Transform are NOT invoked on this path — the routing is fully resolved at emit time by the author code.
- **Quota cap rejection** — `chat.message` is capped at 2 subscriptions per principal at `events.Register` time. The third subscribe returns `-32013 TooManySubscriptions` with `data:{limit:"subscriptions", max:2}` — the spec wire shape any other consumer can rely on.
- **Cursorless source wire shape** — `presence.changed` polls always return empty + `cursor:null`.

## Architecture

Single process. Three synthetic feeders run as goroutines inside the server binary:

- `runChatFeeder` yields onto a `events.YieldingSource[ChatMessageData]` every 2s. Library broadcasts; `EventDef.Match` filters per subscriber.
- `runAlertFeeder` yields onto a `events.YieldingSource[AlertData]` every 4s. Library broadcasts; `Match` filters and `Transform` shapes per subscriber.
- `runPresenceFeeder` consults the watch-list registry, picks matching subscriptions, and calls `events.EmitToSubscription` per matched (sub, transition) pair — the library's broadcast path is never invoked for presence.

The walkthrough additionally posts deterministic events via a `/inject` endpoint so the live steps don't depend on the random feeder cadence.

## File layout (per [`examples/events/CONVENTIONS.md`](../CONVENTIONS.md))

```
kitchen-sink/
├── main.go              # server + walkthrough entry point
├── events.go            # three EventDefs with Match / Transform / OnSubscribe hooks
├── synthetic_chat.go    # chat feeder + injection helper
├── synthetic_alert.go   # alert feeder + injection helper
├── synthetic_presence.go # presence feeder + EmitToSubscription routing
├── handlers.go          # MCP resources for cursored sources
├── walkthrough.go       # demokit step definitions
├── e2e_test.go          # in-process tests (no Docker needed)
├── Makefile
├── README.md            # this file
├── WALKTHROUGH.md       # generated by `make readme`
├── go.mod
└── go.sum
```

## Tests

Seven in-process tests covering each per-sub feature:

```bash
make test
```

| Test | Pins |
|---|---|
| `TestMatch_ChannelFiltering_RoutesOnlyMatchingEventsToEachSub` | Two subs different `params.channel`; only matching events delivered; no cross-talk. |
| `TestTransform_RedactsPII_OnlyForOptedInSubscriber` | Same upstream event yields different bytes per sub based on `params.redact_pii`. |
| `TestMatch_FiltersBeforeTransform` | Ordering: Match gate runs before Transform. |
| `TestOnSubscribe_RegistersWatchList` | OnSubscribe populates the per-sub registry. |
| `TestEmitToSubscription_RoutesOnlyToWatchingSubscriptions` | Targeted emit reaches only watching subs; unmatched transitions reach nobody. |
| `TestOnUnsubscribe_ClearsWatchList` | OnUnsubscribe purges the registry entry on stream stop. |
| `TestQuota_RejectsBeyondCapWithStructuredError` | Pins the `-32013` + `{limit, max}` wire shape so 407 stage 4 can reference it. |

## Library APIs this demo exercises

| Surface | Where |
|---|---|
| `EventDef.Match` / `Transform` / `OnSubscribe` / `OnUnsubscribe` | `experimental/ext/events/events.go` |
| `HookContext` | `experimental/ext/events/hooks.go` |
| `events.EmitToSubscription` | `experimental/ext/events/emit_targeted.go` |
| `events.Quota` + `WithMaxSubscriptionsPerPrincipal` | `experimental/ext/events/quota.go` |
| `events.SubscriptionIndex` | `experimental/ext/events/subscription_index.go` |
| `eventsclient.StreamOptions.Params` (new in this PR) | `experimental/ext/events/clients/go/stream.go` |
| `eventsclient.SubscribeOptions.Params` (new in this PR) | `experimental/ext/events/clients/go/subscription.go` |

## Out of scope (deliberately)

- Real third-party integrations — synthetic upstreams keep the focus on the protocol shape.
- Multi-process kitchen-sink — explicitly out of scope per [issue 406](https://github.com/panyam/mcpkit/issues/406).
- Docker / docker-compose — see `whole-enchilada/` for the deploy axis.
- Webhook delivery step in the walkthrough — push streams already make per-sub Match / Transform visible without the extra setup.

## See also

- [`examples/events/CONVENTIONS.md`](../CONVENTIONS.md) — the events-demo family conventions.
- [`experimental/ext/events/README.md`](../../../experimental/ext/events/README.md) — full library overview.
- [`experimental/ext/events/HTTP_SOURCE.md`](../../../experimental/ext/events/HTTP_SOURCE.md) — the multi-tier source pattern used by whole-enchilada.
