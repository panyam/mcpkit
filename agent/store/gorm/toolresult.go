package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// DefaultToolResultTable is the table a ToolResultStore uses when
// WithToolResultTableName is not set.
const DefaultToolResultTable = "agent_tool_results"

// toolResultRow is one offloaded result, JSON-encoded. StoredAt anchors
// retention (see PruneExpired). The table name is resolved at runtime
// via db.Table so a deployment can point the store at a table of its
// choosing inside an existing database.
type toolResultRow struct {
	Ref      string    `gorm:"primaryKey"`
	Body     string    `gorm:"type:jsonb;not null"`
	StoredAt time.Time `gorm:"not null;index"`
}

// ToolResultStore is the durable GORM backend for agent.ToolResultStore
// (Postgres or SQLite). One row per ref in a configurable table, so it
// drops into an already-running database alongside other tables or
// stands alone. Retention is a caller-driven GC sweep (PruneExpired over
// a WithToolResultRetention window); eviction is safe because the
// interface's unknown-ref contract degrades gracefully at read time.
type ToolResultStore struct {
	db        *gorm.DB
	table     string
	retention time.Duration
}

var _ agent.ToolResultStore = (*ToolResultStore)(nil)

type toolResultConfig struct {
	skipAutoMigrate bool
	table           string
	retention       time.Duration
}

// ToolResultOption customizes a ToolResultStore. Distinct from the
// RunStore Option type so the two stores' options never mix.
type ToolResultOption func(*toolResultConfig)

// WithToolResultTableName points the store at a specific table, so it can
// live inside an existing schema next to unrelated tables. Empty keeps
// DefaultToolResultTable.
func WithToolResultTableName(name string) ToolResultOption {
	return func(c *toolResultConfig) {
		if name != "" {
			c.table = name
		}
	}
}

// WithoutToolResultAutoMigrate disables the AutoMigrate call at
// construction, for deployments whose schema is managed out of band.
func WithoutToolResultAutoMigrate() ToolResultOption {
	return func(c *toolResultConfig) { c.skipAutoMigrate = true }
}

// WithToolResultRetention sets the age past which PruneExpired deletes a
// stored result. Zero (the default) means keep forever (PruneExpired is
// then a no-op). Retention is caller-driven: the store does not run a
// background sweeper, so a host or operator calls PruneExpired on its own
// cadence.
func WithToolResultRetention(window time.Duration) ToolResultOption {
	return func(c *toolResultConfig) { c.retention = window }
}

// NewToolResultStore returns a store over db, running AutoMigrate for the
// results table unless WithoutToolResultAutoMigrate is passed. The db
// handle is shared, not owned.
func NewToolResultStore(db *gorm.DB, opts ...ToolResultOption) (*ToolResultStore, error) {
	cfg := &toolResultConfig{table: DefaultToolResultTable}
	for _, o := range opts {
		o(cfg)
	}
	if !cfg.skipAutoMigrate {
		if err := db.Table(cfg.table).AutoMigrate(&toolResultRow{}); err != nil {
			return nil, fmt.Errorf("gormstore: automigrate tool results: %w", err)
		}
	}
	return &ToolResultStore{db: db, table: cfg.table, retention: cfg.retention}, nil
}

// PutToolResult implements agent.ToolResultStore. Storing the same ref
// twice upserts; callers never reuse a ref.
func (s *ToolResultStore) PutToolResult(ctx context.Context, req agent.PutToolResultRequest) (agent.PutToolResultResponse, error) {
	body, err := json.Marshal(req.Result)
	if err != nil {
		return agent.PutToolResultResponse{}, fmt.Errorf("gormstore: encoding tool result: %w", err)
	}
	row := toolResultRow{Ref: req.Ref, Body: string(body), StoredAt: time.Now().UTC()}
	err = s.db.WithContext(ctx).Table(s.table).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "ref"}}, UpdateAll: true}).
		Create(&row).Error
	if err != nil {
		return agent.PutToolResultResponse{}, err
	}
	return agent.PutToolResultResponse{}, nil
}

// GetToolResult implements agent.ToolResultStore. A missing row (never
// stored, or pruned) is Found=false, not an error.
func (s *ToolResultStore) GetToolResult(ctx context.Context, req agent.GetToolResultRequest) (agent.GetToolResultResponse, error) {
	var row toolResultRow
	err := s.db.WithContext(ctx).Table(s.table).First(&row, "ref = ?", req.Ref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent.GetToolResultResponse{}, nil
	}
	if err != nil {
		return agent.GetToolResultResponse{}, err
	}
	var result core.ToolResult
	if err := json.Unmarshal([]byte(row.Body), &result); err != nil {
		return agent.GetToolResultResponse{}, fmt.Errorf("gormstore: corrupt tool result %q: %w", req.Ref, err)
	}
	return agent.GetToolResultResponse{Result: result, Found: true}, nil
}

// PruneExpired deletes results older than the WithToolResultRetention
// window and returns how many rows it removed. A no-op (0, nil) when no
// retention window is configured. Call it on a cadence a host or cron
// owns; the store never sweeps on its own.
func (s *ToolResultStore) PruneExpired(ctx context.Context) (int, error) {
	if s.retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-s.retention)
	res := s.db.WithContext(ctx).Table(s.table).Where("stored_at < ?", cutoff).Delete(&toolResultRow{})
	return int(res.RowsAffected), res.Error
}
