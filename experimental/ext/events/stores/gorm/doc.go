// Package gormstore provides GORM-backed implementations of the
// experimental/ext/events storage seams (WebhookStore, QuotaStore) for
// multi-replica deployments where in-memory state can't be shared.
//
// The package keeps GORM out of the events parent module — users who
// don't need a persistent backend never pull pgx, mattn-sqlite3, or
// gorm into their build graph.
//
// One GORM database connection backs both stores. Dialects validated
// in CI: Postgres (production target for the whole-enchilada demo) and
// SQLite (no-Docker default for fast local tests). MySQL is untested
// today but the generated SQL is dialect-portable; a Reserve-quota
// dialect branch would be needed to ship MySQL with the atomic-CAS
// guarantee intact.
//
// Schema management uses GORM AutoMigrate on store construction —
// forward-only, idempotent, no separate migrations runner. Operators
// running production deployments are encouraged to pre-create the
// schema explicitly and skip AutoMigrate via the WithoutAutoMigrate
// option; AutoMigrate is the demo / dev-mode default, not a long-term
// schema-evolution story.
//
// Tenant isolation today is by-principal-encoding: callers compose
// principal as e.g. "<tenant>/<subject>" upstream of the store; the
// store treats principal as opaque. Stage-2 of the prod-events demo
// (issue 637) wires Keycloak introspection into a typed auth ctx that
// produces such principals; at that point we may evolve the Request
// types to carry tenant explicitly. The interface stays
// backwards-compatible — Request structs accept new fields without
// breaking existing call sites.
package gormstore
