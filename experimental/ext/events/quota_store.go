package events

import "github.com/panyam/mcpkit/stores"

// The QuotaStore reservation-counter seam lives in the root stores package
// (github.com/panyam/mcpkit/stores) so any consumer of the generic
// (Principal, Key) → counter shape can use it, not just events. This file
// re-exports the types as events-package aliases for source compatibility and
// keeps events' own WithQuotaStore option. The events Quota wrapper is the
// one adapter that maps its event-name bucketing onto the generic Key field
// (see quota.go call sites). Issue 774.

// QuotaStore is the storage seam behind Quota's per-(principal, key)
// reservation counts. Alias of stores.QuotaStore.
type QuotaStore = stores.QuotaStore

// Request/response types are aliases of the root stores types. Note the field
// rename from the pre-0.4 events-local shape: EventName -> Key. External
// QuotaStore implementations that switched on req.EventName must read req.Key.
type (
	ReserveQuotaRequest  = stores.ReserveQuotaRequest
	ReserveQuotaResponse = stores.ReserveQuotaResponse
	ReleaseQuotaRequest  = stores.ReleaseQuotaRequest
	ReleaseQuotaResponse = stores.ReleaseQuotaResponse
	CountQuotaRequest    = stores.CountQuotaRequest
	CountQuotaResponse   = stores.CountQuotaResponse
)

// NewInMemoryQuotaStore returns the default in-memory QuotaStore. Delegates to
// the root stores implementation.
func NewInMemoryQuotaStore() QuotaStore {
	return stores.NewInMemoryQuotaStore()
}

// WithQuotaStore overrides the wrapper's storage implementation. Passing nil is
// treated as "use the default in-memory store" — the constructor materializes a
// fresh NewInMemoryQuotaStore in that case.
func WithQuotaStore(s QuotaStore) QuotaOption {
	return func(q *Quota) {
		if s != nil {
			q.store = s
		}
	}
}
