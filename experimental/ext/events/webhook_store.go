package events

import "context"

// WebhookStore is the storage seam behind WebhookRegistry. The default
// in-memory implementation (NewInMemoryWebhookStore) matches the
// registry's historical behavior exactly; alternative implementations
// plug in via WithWebhookStore. The Postgres-backed implementation
// lands in a follow-up issue (#630); a Redis-backed implementation
// would live alongside it.
//
// API shape: every method follows the gRPC-style
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// convention so the same interface can sit directly behind a gRPC
// service in a future remote-store deployment without reshaping
// callers. ctx threads cancellation, deadlines, and trace context
// (SEP-414 / core.TracerProvider) into the store; Request and Response
// types are extensible — adding a PageToken to ListWebhooksRequest, for
// example, won't change the method signature.
//
// Error semantics: the error return is reserved for storage-layer
// failures (connection drops, transaction conflicts, serialization
// issues). Application-level absence ("the row didn't exist") is
// reported via Found / Removed booleans on the response — a caller
// never has to interpret error sentinels for routine flow control.
//
// Concurrency contract: WebhookRegistry calls these methods under its
// own mutex today, so the in-memory implementation does not take
// internal locks. Implementations that share state across multiple
// registry instances (Postgres, Redis) take their own locks or use
// transaction semantics — the registry's local mutex is not enough at
// that scale.
//
// Naming convention: each storage concern in experimental/ext/events/
// has its own narrow interface (WebhookStore here; QuotaStore,
// CursorStore, and SubscriptionIndex-storage land in sibling PRs).
// Backends implement the subset they need — there is no umbrella
// EventsStore. See STORAGE_SEAMS.md for the full convention.
type WebhookStore interface {
	GetWebhook(ctx context.Context, req GetWebhookRequest) (GetWebhookResponse, error)
	SaveWebhook(ctx context.Context, req SaveWebhookRequest) (SaveWebhookResponse, error)
	DeleteWebhook(ctx context.Context, req DeleteWebhookRequest) (DeleteWebhookResponse, error)
	ListWebhooks(ctx context.Context, req ListWebhooksRequest) (ListWebhooksResponse, error)
	CountWebhooks(ctx context.Context, req CountWebhooksRequest) (CountWebhooksResponse, error)
}

// GetWebhookRequest identifies a single webhook target by canonical key.
type GetWebhookRequest struct {
	// CanonicalKey is the spec's canonical-tuple bytes — opaque to the
	// store; the registry composes it from (principal, name, params, url).
	CanonicalKey []byte
}

// GetWebhookResponse carries the lookup result. Found is false when no
// row matched; the Target field is the zero value in that case.
type GetWebhookResponse struct {
	Target WebhookTarget
	Found  bool
}

// SaveWebhookRequest upserts a target — implementations overwrite any
// existing row keyed by Target.CanonicalKey without a separate signal.
type SaveWebhookRequest struct {
	Target WebhookTarget
}

// SaveWebhookResponse is empty today; reserved for future fields (e.g.,
// a write-conflict version stamp) without breaking the interface.
type SaveWebhookResponse struct{}

// DeleteWebhookRequest identifies a target to remove by canonical key.
type DeleteWebhookRequest struct {
	CanonicalKey []byte
}

// DeleteWebhookResponse carries the removed target plus a Found flag
// so callers can fire onRemove hooks without a separate Get round
// trip. Found is false when no row matched; the Removed field is the
// zero value in that case.
type DeleteWebhookResponse struct {
	Removed WebhookTarget
	Found   bool
}

// ListWebhooksRequest is empty today; pagination fields land here when
// a real backend needs them.
type ListWebhooksRequest struct{}

// ListWebhooksResponse carries every stored target as a snapshot slice.
// Implementations MUST copy out, not return their internal storage —
// callers iterate without holding any external lock. Ordering is
// unspecified.
type ListWebhooksResponse struct {
	Targets []WebhookTarget
}

// CountWebhooksRequest is empty today.
type CountWebhooksRequest struct{}

// CountWebhooksResponse carries the current target count. Implementations
// MAY approximate Count for performance (returning a value off by a few
// when contention is high) — callers use it for capacity hints only,
// never for correctness.
type CountWebhooksResponse struct {
	Count int
}

// NewInMemoryWebhookStore returns the default in-memory storage
// implementation — a plain map[string]WebhookTarget with no internal
// locking. Suitable for single-process deployments and the default
// when WithWebhookStore is not configured. Multi-replica deployments
// plug in a shared backend (e.g., the Postgres-backed implementation
// in #630). ctx is accepted on every method to honor the convention,
// but the in-memory implementation never observes cancellation —
// every call is a synchronous map operation.
func NewInMemoryWebhookStore() WebhookStore {
	return &inMemoryWebhookStore{
		targets: make(map[string]WebhookTarget),
	}
}

// WithWebhookStore overrides the registry's storage implementation.
// Passing nil is treated as "use the default in-memory store" — the
// constructor materializes a fresh NewInMemoryWebhookStore in that case.
// Explicit configuration is recommended for clarity at registry
// construction sites.
func WithWebhookStore(s WebhookStore) WebhookOption {
	return func(r *WebhookRegistry) {
		if s != nil {
			r.store = s
		}
	}
}

// inMemoryWebhookStore is the default WebhookStore implementation.
// Unlocked because WebhookRegistry calls every method under its own
// mutex — the store sees a single goroutine at a time.
type inMemoryWebhookStore struct {
	targets map[string]WebhookTarget
}

func (s *inMemoryWebhookStore) GetWebhook(_ context.Context, req GetWebhookRequest) (GetWebhookResponse, error) {
	t, ok := s.targets[string(req.CanonicalKey)]
	if !ok {
		return GetWebhookResponse{}, nil
	}
	return GetWebhookResponse{Target: t, Found: true}, nil
}

func (s *inMemoryWebhookStore) SaveWebhook(_ context.Context, req SaveWebhookRequest) (SaveWebhookResponse, error) {
	s.targets[string(req.Target.CanonicalKey)] = req.Target
	return SaveWebhookResponse{}, nil
}

func (s *inMemoryWebhookStore) DeleteWebhook(_ context.Context, req DeleteWebhookRequest) (DeleteWebhookResponse, error) {
	t, ok := s.targets[string(req.CanonicalKey)]
	if !ok {
		return DeleteWebhookResponse{}, nil
	}
	delete(s.targets, string(req.CanonicalKey))
	return DeleteWebhookResponse{Removed: t, Found: true}, nil
}

func (s *inMemoryWebhookStore) ListWebhooks(_ context.Context, _ ListWebhooksRequest) (ListWebhooksResponse, error) {
	out := make([]WebhookTarget, 0, len(s.targets))
	for _, t := range s.targets {
		out = append(out, t)
	}
	return ListWebhooksResponse{Targets: out}, nil
}

func (s *inMemoryWebhookStore) CountWebhooks(_ context.Context, _ CountWebhooksRequest) (CountWebhooksResponse, error) {
	return CountWebhooksResponse{Count: len(s.targets)}, nil
}
