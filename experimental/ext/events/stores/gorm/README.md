# gormstore — GORM-backed events stores

GORM-backed implementations of the `experimental/ext/events` storage seams for multi-replica deployments where the in-memory default can't share state across processes.

This is a **separate Go module**. The events library does not import GORM — deployments that don't need a persistent backend never pull GORM, pgx, or sqlite into their build graph.

## Stores

| Seam | Status |
|---|---|
| `events.WebhookStore` | implemented |
| `events.QuotaStore` | implemented |
| `events.SubscriptionIndexStore` | not implemented — closures captured by `Deliver func(Event)` are intrinsically per-replica; cross-replica routing is the [Emitter](../../emitter.go) seam's job, not the index's |
| Cursor positions | not implemented — the server doesn't track per-subscription cursors today (the events module's design treats cursors as recoverable from the message store) |

## Supported dialects

Validated in CI (`make testpg` runs the full matrix):

- **Postgres** — production target for the [whole-enchilada demo](../../../../../examples/whole-enchilada/events/).
- **SQLite** — no-Docker default for fast local tests. CGO-based via `gorm.io/driver/sqlite`.

MySQL is untested. The generated SQL for `ReserveQuota` uses `SELECT FOR UPDATE` row locks which work on Postgres and MySQL but are a no-op on SQLite (the whole transaction serializes by default). Anyone wanting MySQL today should run the conformance suite themselves to verify behavioral parity before relying on it.

## Usage

```go
import (
    "github.com/panyam/mcpkit/experimental/ext/events"
    gormstore "github.com/panyam/mcpkit/experimental/ext/events/stores/gorm"
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
)

db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
if err != nil { /* handle */ }

webhooks, err := gormstore.NewWebhookStore(db)
if err != nil { /* handle */ }
quotas, err := gormstore.NewQuotaStore(db)
if err != nil { /* handle */ }

reg := events.NewWebhookRegistry(events.WithWebhookStore(webhooks))
quota := events.NewQuota(events.WithQuotaStore(quotas))
```

`AutoMigrate` runs automatically on construction. For production deployments where schema changes are managed by an out-of-band migration tool, pass `gormstore.WithoutAutoMigrate()`:

```go
webhooks, err := gormstore.NewWebhookStore(db, gormstore.WithoutAutoMigrate())
```

## Schema

```sql
CREATE TABLE webhooks (
    canonical_key            BYTEA PRIMARY KEY,
    id                       TEXT NOT NULL,
    url                      TEXT NOT NULL,
    secret                   TEXT NOT NULL,
    expires_at               TIMESTAMPTZ NOT NULL,
    max_age_seconds          BIGINT NOT NULL,
    event_name               TEXT NOT NULL,
    principal                TEXT NOT NULL,
    params                   JSONB,
    status_active            BOOLEAN NOT NULL,
    status_last_delivery_at  TIMESTAMPTZ,
    status_last_error        TEXT NOT NULL,
    status_failed_since      TIMESTAMPTZ,
    failure_count            BIGINT NOT NULL
);
CREATE INDEX idx_webhooks_principal_event ON webhooks(principal, event_name);

CREATE TABLE quota_counters (
    principal   TEXT NOT NULL,
    event_name  TEXT NOT NULL,
    count       BIGINT NOT NULL,
    PRIMARY KEY (principal, event_name)
);
```

The primary key for `webhooks` is the spec's canonical-tuple bytes (BYTEA on Postgres, BLOB on SQLite). Status fields are flattened rather than nested JSON so operators can query delivery health (`WHERE status_active = false`) without JSON extraction.

## Secret-at-rest

`webhooks.secret` is stored as the raw HMAC signing material — the same bytes the client supplied on subscribe. Operators relying on disk-level encryption (encrypted EBS volumes, Cloud SQL CMEK, etc.) for confidentiality at rest can stop here. Stronger protection (pgcrypto column-level encryption, KMS-wrapped secrets) is a future-hardening follow-up; for the prod-events demo's threat model, disk-level encryption is the documented posture.

## Tenant isolation

Tenant-id is not a first-class column. The events library encodes tenancy upstream into `principal` (e.g. `principal = "<tenant>/<subject>"`) so the same canonical key under different tenants is two distinct subscriptions; quota counters scope by the same encoded principal. The store treats principal as opaque.

This matches the demo's acceptance criteria (Tenant A and Tenant B see fully isolated subscription state and quota caps). If first-class per-tenant operator queries become a hard requirement, the Request types are structs — adding a `TenantID` field later is backwards-compatible.

## Tests

```bash
# Default: in-memory + sqlite. No Docker required.
just test

# Full conformance matrix including Postgres. Boots a container on port 5434.
just testpg

# Long-running Postgres for ad-hoc test iteration.
just updb
just testpg     # repeats fast against the running container
just downdb
```

The conformance suite runs the same assertion body against every available backend (in-memory, sqlite, postgres). `TestQuotaStore_ConcurrentReserveAtomicity` skips the in-memory backend by design: the in-memory store's contract is "wrapper-locked, not self-locked", so racing it directly is a no-test.

## Why GORM

Two reasons over hand-written `database/sql`:

- **Multi-dialect from one impl.** Adding SQLite costs us essentially nothing — operators get a no-Docker local default. Adding MySQL later costs only a `ReserveQuota` dialect branch.
- **Schema-from-struct.** `AutoMigrate` is the demo's default; pre-baked SQL migrations are the production posture. Both share one source of truth (the model struct tags).

The atomic CAS for `ReserveQuota` uses an explicit transaction with `clause.Locking{Strength: "UPDATE"}` rather than GORM's `clause.OnConflict` helper because the latter loses behavioral parity across dialects when the conflict clause refuses to update — we need the "deny + return current count" branch, not "silently no-op".
