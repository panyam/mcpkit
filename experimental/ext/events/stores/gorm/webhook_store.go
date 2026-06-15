package gormstore

import (
	"context"
	"errors"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// webhookRow is the GORM model for events.WebhookTarget. Status fields
// are flattened (not nested JSON) so operators can query
// status_active / status_last_error directly. Params is stored as JSON
// because its shape is open-ended subscription params (see the spec's
// canonicalization rules in events.WebhookTarget).
//
// IMPORTANT: no `default:` tags on bool / int / string fields.
// GORM treats `default:` as "if Go field is zero, let the DB fill in
// the default" — which silently omits StatusActive=false (suspended
// targets) from INSERTs, corrupting round-trips. Schema-level NOT NULL
// is enforced via column type, not via the GORM default tag.
type webhookRow struct {
	CanonicalKey []byte `gorm:"primaryKey"`
	ID           string `gorm:"not null"`
	URL          string `gorm:"not null"`
	// Secret is stored as-is; the store relies on disk-level encryption
	// rather than column-level encryption (pgcrypto is documented as
	// future hardening in README.md).
	Secret string `gorm:"not null"`
	// ExpiresAt is nullable: NULL means no-expiry per spec PR1 commit
	// 99f3589c §"Subscription TTL". The events package's
	// WebhookTarget.ExpiresAt mirrors this with *time.Time.
	ExpiresAt     *time.Time
	MaxAgeSeconds int `gorm:"not null"`
	EventName     string         `gorm:"not null;index:idx_webhooks_principal_event"`
	Principal     string         `gorm:"not null;index:idx_webhooks_principal_event"`
	Arguments     map[string]any `gorm:"serializer:json"` // column renamed from params: spec PR1 commit 082166f0
	// DeliveryStatus flattened — kept queryable, ordering preserved with
	// the events.DeliveryStatus struct definition.
	//
	// StatusThrottled + StatusRetryAfterMs added with spec PR1 commit
	// 21be9c31 (deliveryStatus throttled + retryAfterMs).
	StatusActive         bool `gorm:"not null"`
	StatusLastDeliveryAt *time.Time
	StatusLastError      string `gorm:"not null"`
	StatusFailedSince    *time.Time
	StatusThrottled      bool `gorm:"not null"`
	StatusRetryAfterMs   *int64
	// FailureCount mirrors events.WebhookTarget.FailureCount — the
	// suspend-state-machine counter that lets restarts preserve
	// per-target delivery health. Was unexported on the events struct
	// before this seam; exported in the same PR that introduced the
	// store so external backends can round-trip it.
	FailureCount int `gorm:"not null"`
}

func (webhookRow) TableName() string { return "webhooks" }

func rowFromTarget(t events.WebhookTarget) webhookRow {
	return webhookRow{
		CanonicalKey:         t.CanonicalKey,
		ID:                   t.ID,
		URL:                  t.URL,
		Secret:               t.Secret,
		ExpiresAt:            t.ExpiresAt,
		MaxAgeSeconds:        t.MaxAgeSeconds,
		EventName:            t.EventName,
		Principal:            t.Principal,
		Arguments:            t.Arguments,
		StatusActive:         t.Status.Active,
		StatusLastDeliveryAt: t.Status.LastDeliveryAt,
		StatusLastError:      string(t.Status.LastError),
		StatusFailedSince:    t.Status.FailedSince,
		StatusThrottled:      t.Status.Throttled,
		StatusRetryAfterMs:   t.Status.RetryAfterMs,
		FailureCount:         t.FailureCount,
	}
}

func targetFromRow(r webhookRow) events.WebhookTarget {
	return events.WebhookTarget{
		CanonicalKey:  r.CanonicalKey,
		ID:            r.ID,
		URL:           r.URL,
		Secret:        r.Secret,
		ExpiresAt:     r.ExpiresAt,
		MaxAgeSeconds: r.MaxAgeSeconds,
		EventName:     r.EventName,
		Principal:     r.Principal,
		Arguments:     r.Arguments,
		Status: events.DeliveryStatus{
			Active:         r.StatusActive,
			LastDeliveryAt: r.StatusLastDeliveryAt,
			LastError:      events.DeliveryErrorBucket(r.StatusLastError),
			FailedSince:    r.StatusFailedSince,
			Throttled:      r.StatusThrottled,
			RetryAfterMs:   r.StatusRetryAfterMs,
		},
		FailureCount: r.FailureCount,
	}
}

// webhookStore is the GORM-backed implementation of events.WebhookStore.
// Concurrency safety comes from the database (Postgres row locks,
// SQLite full-transaction serialization) — the registry's local mutex
// is insufficient at multi-replica scale, which is precisely why this
// seam exists.
type webhookStore struct {
	db *gorm.DB
}

// NewWebhookStore returns a GORM-backed WebhookStore. The supplied
// *gorm.DB must point at a writable connection; AutoMigrate runs on
// construction unless WithoutAutoMigrate is passed. The caller owns
// the connection lifecycle — closing the underlying *sql.DB is the
// caller's responsibility.
func NewWebhookStore(db *gorm.DB, opts ...Option) (events.WebhookStore, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if !cfg.skipAutoMigrate {
		if err := db.AutoMigrate(&webhookRow{}); err != nil {
			return nil, err
		}
	}
	return &webhookStore{db: db}, nil
}

// GetWebhook implements events.WebhookStore. Found is false when no
// row matches; the error return is reserved for storage-layer
// failures.
func (s *webhookStore) GetWebhook(ctx context.Context, req events.GetWebhookRequest) (events.GetWebhookResponse, error) {
	var row webhookRow
	err := s.db.WithContext(ctx).Where("canonical_key = ?", req.CanonicalKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return events.GetWebhookResponse{}, nil
	}
	if err != nil {
		return events.GetWebhookResponse{}, err
	}
	return events.GetWebhookResponse{Target: targetFromRow(row), Found: true}, nil
}

// SaveWebhook implements events.WebhookStore as an upsert keyed on
// CanonicalKey. UpdateAll replaces every column except the primary key
// so partial fields (Status, FailureCount) round-trip on every save
// the registry performs.
func (s *webhookStore) SaveWebhook(ctx context.Context, req events.SaveWebhookRequest) (events.SaveWebhookResponse, error) {
	row := rowFromTarget(req.Target)
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "canonical_key"}},
			UpdateAll: true,
		}).
		Create(&row).Error
	if err != nil {
		return events.SaveWebhookResponse{}, err
	}
	return events.SaveWebhookResponse{}, nil
}

// DeleteWebhook implements events.WebhookStore with a Get-then-Delete
// inside a transaction so the response carries the removed row (used
// by onRemove hook firing) without a second round trip from the
// caller.
func (s *webhookStore) DeleteWebhook(ctx context.Context, req events.DeleteWebhookRequest) (events.DeleteWebhookResponse, error) {
	var resp events.DeleteWebhookResponse
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row webhookRow
		err := tx.Where("canonical_key = ?", req.CanonicalKey).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Where("canonical_key = ?", req.CanonicalKey).Delete(&webhookRow{}).Error; err != nil {
			return err
		}
		resp = events.DeleteWebhookResponse{Removed: targetFromRow(row), Found: true}
		return nil
	})
	if err != nil {
		return events.DeleteWebhookResponse{}, err
	}
	return resp, nil
}

// ListWebhooks implements events.WebhookStore as a full-table scan.
// Acceptable for the scale this seam targets today (one events
// deployment, ~thousands of webhooks); pagination fields land on the
// Request type when a real backend needs them.
func (s *webhookStore) ListWebhooks(ctx context.Context, _ events.ListWebhooksRequest) (events.ListWebhooksResponse, error) {
	var rows []webhookRow
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return events.ListWebhooksResponse{}, err
	}
	out := make([]events.WebhookTarget, len(rows))
	for i, r := range rows {
		out[i] = targetFromRow(r)
	}
	return events.ListWebhooksResponse{Targets: out}, nil
}

// CountWebhooks implements events.WebhookStore. The contract permits
// approximation but the SQL backend returns an exact count — callers
// already document Count as a capacity hint, not a correctness check.
func (s *webhookStore) CountWebhooks(ctx context.Context, _ events.CountWebhooksRequest) (events.CountWebhooksResponse, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&webhookRow{}).Count(&n).Error; err != nil {
		return events.CountWebhooksResponse{}, err
	}
	return events.CountWebhooksResponse{Count: int(n)}, nil
}
