package gormstore

import (
	"context"
	"errors"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// quotaRow is the GORM model for quota counters. The primary key is
// (Principal, Key) — the same composite key the in-memory store uses.
// The column is pinned to quota_key because GORM's default name for a Key
// field ("key") is a SQL reserved word. Field renamed from EventName in the
// #774 lift; fresh-deploys-only migration convention (recreate the table).
type quotaRow struct {
	Principal string `gorm:"primaryKey"`
	Key       string `gorm:"column:quota_key;primaryKey"`
	// No `default:0` tag — GORM would interpret it as "if Count is 0,
	// let the DB pick the default", omitting genuine zero values from
	// INSERTs and breaking the ReserveQuota CAS semantics.
	Count int `gorm:"not null"`
}

func (quotaRow) TableName() string { return "quota_counters" }

// quotaStore is the GORM-backed implementation of events.QuotaStore.
// The atomic CAS for ReserveQuota relies on the database's transaction
// semantics — Postgres uses SELECT FOR UPDATE row locks; SQLite
// serializes the whole transaction. Both backends preserve the
// "compare current count against Max, increment only if strictly
// less" invariant under concurrent calls.
type quotaStore struct {
	db *gorm.DB
}

// NewQuotaStore returns a GORM-backed QuotaStore. AutoMigrate runs on
// construction unless WithoutAutoMigrate is passed.
func NewQuotaStore(db *gorm.DB, opts ...Option) (events.QuotaStore, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if !cfg.skipAutoMigrate {
		if err := db.AutoMigrate(&quotaRow{}); err != nil {
			return nil, err
		}
	}
	return &quotaStore{db: db}, nil
}

// ReserveQuota implements events.QuotaStore as an atomic check-and-set.
// Wrapped in a transaction with SELECT FOR UPDATE so concurrent
// reservers see the same compare-and-increment invariant as the
// in-memory store.
//
// Backend semantics:
//
//   - Postgres: SELECT FOR UPDATE acquires a row lock; concurrent
//     reservers serialize on the lock.
//   - SQLite: the whole transaction serializes by default; FOR UPDATE
//     is parsed but is a no-op. Concurrency safety still holds.
//
// Caller contract is unchanged from the in-memory store: Granted=true
// iff the slot was claimed; Count is the post-increment value when
// Granted, the blocking value otherwise.
func (s *quotaStore) ReserveQuota(ctx context.Context, req events.ReserveQuotaRequest) (events.ReserveQuotaResponse, error) {
	var resp events.ReserveQuotaResponse
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row quotaRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("principal = ? AND quota_key = ?", req.Principal, req.Key).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// First reservation for this tuple — create the row with count=1.
			// req.Max is asserted > 0 by the wrapper before the call.
			row = quotaRow{Principal: req.Principal, Key: req.Key, Count: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			resp = events.ReserveQuotaResponse{Granted: true, Count: 1}
			return nil
		}
		if err != nil {
			return err
		}
		if row.Count >= req.Max {
			resp = events.ReserveQuotaResponse{Granted: false, Count: row.Count}
			return nil
		}
		row.Count++
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		resp = events.ReserveQuotaResponse{Granted: true, Count: row.Count}
		return nil
	})
	if err != nil {
		return events.ReserveQuotaResponse{}, err
	}
	return resp, nil
}

// ReleaseQuota implements events.QuotaStore. Decrements the count if
// it is currently > 0; release-at-zero is a silent no-op (matches the
// in-memory store's contract — double-release shouldn't underflow).
func (s *quotaStore) ReleaseQuota(ctx context.Context, req events.ReleaseQuotaRequest) (events.ReleaseQuotaResponse, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Model(&quotaRow{}).
			Where("principal = ? AND quota_key = ? AND count > 0", req.Principal, req.Key).
			UpdateColumn("count", gorm.Expr("count - 1")).Error
	})
	if err != nil {
		return events.ReleaseQuotaResponse{}, err
	}
	return events.ReleaseQuotaResponse{}, nil
}

// CountQuota implements events.QuotaStore. Inspection-only path —
// callers must not gate correctness on the value (the contract permits
// approximation under contention).
func (s *quotaStore) CountQuota(ctx context.Context, req events.CountQuotaRequest) (events.CountQuotaResponse, error) {
	var row quotaRow
	err := s.db.WithContext(ctx).
		Where("principal = ? AND quota_key = ?", req.Principal, req.Key).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return events.CountQuotaResponse{Count: 0}, nil
	}
	if err != nil {
		return events.CountQuotaResponse{}, err
	}
	return events.CountQuotaResponse{Count: row.Count}, nil
}
