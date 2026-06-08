# Storage seam convention — `experimental/ext/events/`

This file pins the conventions every storage seam in `experimental/ext/events/` follows. The first seam (`WebhookStore`, this PR) establishes the shape; sibling PRs add `QuotaStore` (issue 626), `CursorStore` (issue 628), and the `SubscriptionIndex` storage seam (issue 631) under the same rules. The single source of truth — when in doubt, follow `webhook_store.go`'s precedent.

## Why seams

The MCP Events extension's reference deployment (the `whole-enchilada` demo, issue 407) needs the registry's subscription table, quota counters, cursor positions, and subscription index shared across N replicas. The library's POC implementation kept all of that as in-memory Go maps. Seaming those concerns out into interfaces is the prerequisite for multi-replica state-sharing — Postgres lands in issue 630, Redis lands in 634.

## One interface per concern — no umbrella

Each storage concern has its own narrow interface:

| Interface | Concern | Status |
|---|---|---|
| `WebhookStore` | Webhook subscription CRUD (canonical key → target) | landed (627 / PR 671) |
| `QuotaStore` | Reservation counts per (principal, eventName) | landed (626) |
| `CursorStore` | Per-subscription persisted cursors | lands in 628 |
| `SubscriptionIndexStore` | Subscription-id ↔ deliver-fn lookup table | landed (631) |

No umbrella `EventsStore` interface. The reasons:

- **Backends pick the subset they need.** A Redis backend implements `QuotaStore` only (Redis pubsub + counters); a Postgres backend implements `WebhookStore` + `QuotaStore` + `CursorStore` (durable rows); a future Kafka backend might implement the SubscriptionIndex seam alone. An umbrella interface forces every backend to either implement everything or carry no-op methods that lie about their semantics.
- **Method conflicts dodge.** Different storage concerns name their primary methods the same way (`GetX` / `SaveX` / `ListX`). One umbrella interface either ends up with `GetWebhookByKey` / `GetQuotaByPrincipalEvent` / etc. (verbose) or generic `Get` / `Save` (untyped). Narrow interfaces keep the types crisp.
- **Independent evolution.** Adding a method to `WebhookStore` doesn't ripple through unrelated backends.

## API shape: gRPC-style request / response

Every method on every storage seam follows the same convention:

```go
Method(ctx context.Context, req XRequest) (XResponse, error)
```

- **`ctx` is the first parameter on every method.** Threads cancellation, deadlines, and trace context. The in-memory implementation may ignore it; persistent backends honor it for I/O cancellation and for OTel span propagation (the SEP-414 P1 surface in `core.TracerProvider` is already threaded through ctx, so a Postgres backend gets distributed tracing for free).
- **Request / Response types are named per-operation** (`GetWebhookRequest` / `GetWebhookResponse`, `SaveWebhookRequest` / `SaveWebhookResponse`, etc.). Each type lives in the same file as the interface. Adding a field (pagination tokens, version stamps, hints) doesn't change the method signature — it's backwards-compatible.
- **Error semantics:** the `error` return is reserved for **storage-layer failures** (connection drops, transaction conflicts, serialization issues). Application-level absence ("the row didn't exist") is reported via `Found` / `Removed` booleans on the Response — callers never interpret error sentinels for routine flow control.
- **Forward compatibility with gRPC.** If a future deployment puts a store behind a gRPC service, the interface translates 1:1 to a `.proto` service definition. No reshaping at the consumer.

### Operation verb conventions

Per-seam operations use these verbs uniformly:

| Verb | Semantics |
|---|---|
| `GetX` | Return one entity by primary key. Found=false when absent; no error. |
| `SaveX` | Upsert one entity. Overwrites without conflict. SaveResponse may grow a version field later. |
| `DeleteX` | Remove one entity by key. Found=false when absent; no error. Removed entity is returned for hook firing. |
| `ListX` | Return a snapshot slice. Request is empty today; pagination fields land here later. |
| `CountX` | Return the cardinality. Implementations MAY approximate. |

Per-seam additions follow the same shape: for example, `QuotaStore` may add `IncrementQuotaRequest / Response` for atomic-increment semantics.

## Concurrency contract

The default in-memory implementation does NOT lock internally — its caller (the registry) holds a mutex around every call. Implementations shared across multiple registry instances (Postgres, Redis) handle their own concurrency:

- **In-memory** — single-process, single registry, no internal lock.
- **Postgres** — each call is a transaction or wraps a caller-supplied `*sql.Tx`. Cross-process concurrency safe by construction.
- **Redis** — atomic operations via `INCR` / `WATCH` / `EVAL` per call.

The contract from the registry's perspective is: "I call you under my own mutex; you do whatever locking your backend needs."

## Constructor convention

Every seam exposes a public constructor for the default in-memory implementation, plus an option that lets the registry receive a custom one:

```go
func NewInMemoryWebhookStore() WebhookStore
func WithWebhookStore(s WebhookStore) WebhookOption
```

- The in-memory implementation type is private (`inMemoryWebhookStore`) — operators get a `WebhookStore` interface back, not a concrete pointer.
- The `WithXStore` option accepts `nil` as "fall back to default" — keeps wiring sites tolerant.

## Naming and file layout

- One file per seam: `webhook_store.go`, `quota_store.go`, `cursor_store.go`, `subscription_index_store.go`.
- Tests live alongside: `webhook_store_test.go`, etc.
- The interface, request / response types, default in-memory impl, and option function all live in the seam's file. No splits.

## What the seam is NOT

- **NOT a transaction abstraction.** Backends own their own transaction boundaries. If a future use case needs atomic multi-seam writes (e.g., delete a webhook + decrement its quota in one transaction), that's a separate cross-seam abstraction added on top.
- **NOT a caching layer.** Backends decide their own caching policy. The registry talks to the seam directly.
- **NOT a migration tool.** Schema changes on a backend (e.g., adding columns to a Postgres `webhook` table) are operator concerns, not seam concerns.

## Adding a new seam

1. Define the interface + Request/Response types in a single file named after the concern.
2. Provide a `NewInMemoryX()` constructor and a private `inMemoryX` implementation.
3. Provide a `WithXStore` option on the consumer's constructor.
4. Plumb the consumer's call sites through the interface — no direct field access to the old in-memory state.
5. Add the seam's row to the table above in this doc.

The other seams (#626, #628, #631) follow this template. Reviews check against this file.

## Adjacent seams (not storage, same code conventions)

| Interface | Concern | Status |
|---|---|---|
| `Emitter` | Output-side seam: "given an event, deliver it." Dual of `EventSource`. Default `NewLocalEmitter` matches today's single-replica behavior; multi-replica deployments compose with `NewCompositeEmitter` to add peer fanout. Receive-side cross-replica reuses the existing `HTTPSource` pattern — no new "Subscribe" API needed. | landed (629) |

`Emitter` is not a storage seam (it doesn't persist anything), but follows the same `ctx`-first method shape and lives in its own file (`emitter.go`) per the same conventions. Listed here so future readers see the full set of seams in one place.
