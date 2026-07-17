package gormstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/panyam/mcpkit/agent"
)

// runRow is a run's identity and lineage; its presence is the run's
// existence marker.
type runRow struct {
	ID        string    `gorm:"primaryKey"`
	ParentID  string    `gorm:"index"`
	CreatedAt time.Time `gorm:"not null"`
}

// TableName pins the table independent of GORM pluralization; operators
// reading psql expect agent_runs.
func (runRow) TableName() string { return "agent_runs" }

// runMessageRow is one agent.Message, JSON-encoded. Seq is a global
// autoincrement: per-run order is "ORDER BY seq", and concurrent
// appends to different runs never contend on a per-run counter.
type runMessageRow struct {
	Seq   int64  `gorm:"primaryKey;autoIncrement"`
	RunID string `gorm:"not null;index:idx_agent_run_messages_run"`
	Body  string `gorm:"type:jsonb;not null"`
}

func (runMessageRow) TableName() string { return "agent_run_messages" }

// runEventRow is one agent.Event, JSON-encoded (see runMessageRow).
type runEventRow struct {
	Seq   int64  `gorm:"primaryKey;autoIncrement"`
	RunID string `gorm:"not null;index:idx_agent_run_events_run"`
	Body  string `gorm:"type:jsonb;not null"`
}

func (runEventRow) TableName() string { return "agent_run_events" }

// RunStore implements agent.RunStore on a GORM-managed database. Safe
// for concurrent use; every multi-statement operation (append batches,
// fork) runs in a transaction, so forks are exact cuts and appends are
// all-or-nothing.
type RunStore struct {
	db *gorm.DB
}

var _ agent.RunStore = (*RunStore)(nil)

// New returns a RunStore over db, running AutoMigrate for the three
// tables unless WithoutAutoMigrate is passed. The db handle is shared,
// not owned: close it wherever it was opened.
func New(db *gorm.DB, opts ...Option) (*RunStore, error) {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	if !cfg.skipAutoMigrate {
		if err := db.AutoMigrate(&runRow{}, &runMessageRow{}, &runEventRow{}); err != nil {
			return nil, fmt.Errorf("gormstore: automigrate: %w", err)
		}
	}
	return &RunStore{db: db}, nil
}

// newRunID generates a collision-resistant random ID for runs created
// without a caller-chosen name; the ON CONFLICT claim still guards it.
func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("gormstore: generating run ID: %w", err)
	}
	return "run-" + hex.EncodeToString(b[:]), nil
}

// claimRun inserts the run row iff absent (ON CONFLICT DO NOTHING) and
// reports whether the claim won.
func claimRun(tx *gorm.DB, row runRow) (bool, error) {
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// CreateRun implements agent.RunStore.
func (s *RunStore) CreateRun(ctx context.Context, req agent.CreateRunRequest) (agent.CreateRunResponse, error) {
	db := s.db.WithContext(ctx)
	if req.RunID != "" {
		won, err := claimRun(db, runRow{ID: req.RunID, CreatedAt: time.Now().UTC()})
		if err != nil {
			return agent.CreateRunResponse{}, err
		}
		return agent.CreateRunResponse{RunID: req.RunID, Created: won}, nil
	}
	for {
		id, err := newRunID()
		if err != nil {
			return agent.CreateRunResponse{}, err
		}
		won, err := claimRun(db, runRow{ID: id, CreatedAt: time.Now().UTC()})
		if err != nil {
			return agent.CreateRunResponse{}, err
		}
		if won {
			return agent.CreateRunResponse{RunID: id, Created: true}, nil
		}
	}
}

// runExists reports whether the run row is present.
func runExists(tx *gorm.DB, id string) (bool, error) {
	err := tx.Select("id").First(&runRow{}, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// appendRows JSON-encodes values into newRow-built rows and inserts
// them in order inside tx, gated on the run existing.
func appendRows[T any](tx *gorm.DB, runID string, values []T, insert func(tx *gorm.DB, runID string, bodies []string) error) (bool, error) {
	ok, err := runExists(tx, runID)
	if err != nil || !ok {
		return false, err
	}
	if len(values) == 0 {
		return true, nil
	}
	bodies := make([]string, len(values))
	for i, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return false, fmt.Errorf("gormstore: encoding entry: %w", err)
		}
		bodies[i] = string(b)
	}
	return true, insert(tx, runID, bodies)
}

// AppendMessages implements agent.RunStore.
func (s *RunStore) AppendMessages(ctx context.Context, req agent.AppendMessagesRequest) (agent.AppendMessagesResponse, error) {
	var found bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		found, err = appendRows(tx, req.RunID, req.Messages, func(tx *gorm.DB, runID string, bodies []string) error {
			rows := make([]runMessageRow, len(bodies))
			for i, b := range bodies {
				rows[i] = runMessageRow{RunID: runID, Body: b}
			}
			return tx.Create(&rows).Error
		})
		return err
	})
	return agent.AppendMessagesResponse{Found: found}, err
}

// AppendEvents implements agent.RunStore.
func (s *RunStore) AppendEvents(ctx context.Context, req agent.AppendEventsRequest) (agent.AppendEventsResponse, error) {
	var found bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		found, err = appendRows(tx, req.RunID, req.Events, func(tx *gorm.DB, runID string, bodies []string) error {
			rows := make([]runEventRow, len(bodies))
			for i, b := range bodies {
				rows[i] = runEventRow{RunID: runID, Body: b}
			}
			return tx.Create(&rows).Error
		})
		return err
	})
	return agent.AppendEventsResponse{Found: found}, err
}

// loadBodies reads a run's log table in seq order and decodes each row.
// A corrupt row is a storage-layer failure, surfaced as an error rather
// than dropped — this log is what sessions resume from.
func loadBodies[T any](tx *gorm.DB, table, runID string) ([]T, error) {
	var bodies []string
	if err := tx.Table(table).Where("run_id = ?", runID).Order("seq").Pluck("body", &bodies).Error; err != nil {
		return nil, err
	}
	if len(bodies) == 0 {
		return nil, nil
	}
	out := make([]T, len(bodies))
	for i, b := range bodies {
		if err := json.Unmarshal([]byte(b), &out[i]); err != nil {
			return nil, fmt.Errorf("gormstore: corrupt %s row %d in run %q: %w", table, i, runID, err)
		}
	}
	return out, nil
}

// LoadRun implements agent.RunStore. The returned Run is freshly
// decoded, so it never aliases store state.
func (s *RunStore) LoadRun(ctx context.Context, req agent.LoadRunRequest) (agent.LoadRunResponse, error) {
	db := s.db.WithContext(ctx)
	var row runRow
	err := db.First(&row, "id = ?", req.RunID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent.LoadRunResponse{}, nil
	}
	if err != nil {
		return agent.LoadRunResponse{}, err
	}
	messages, err := loadBodies[agent.Message](db, "agent_run_messages", req.RunID)
	if err != nil {
		return agent.LoadRunResponse{}, err
	}
	events, err := loadBodies[agent.Event](db, "agent_run_events", req.RunID)
	if err != nil {
		return agent.LoadRunResponse{}, err
	}
	return agent.LoadRunResponse{
		Run: agent.Run{
			ID:        row.ID,
			ParentID:  row.ParentID,
			CreatedAt: row.CreatedAt,
			Messages:  messages,
			Events:    events,
		},
		Found: true,
	}, nil
}

// ForkRun implements agent.RunStore. The whole fork — source check, new
// run claim, and both log copies — runs in one transaction, so the fork
// is an exact cut of the source at commit time.
func (s *RunStore) ForkRun(ctx context.Context, req agent.ForkRunRequest) (agent.ForkRunResponse, error) {
	var resp agent.ForkRunResponse
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok, err := runExists(tx, req.RunID)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		id := req.NewRunID
		if id != "" {
			won, err := claimRun(tx, runRow{ID: id, ParentID: req.RunID, CreatedAt: time.Now().UTC()})
			if err != nil {
				return err
			}
			if !won {
				resp = agent.ForkRunResponse{RunID: id, Found: true, Created: false}
				return nil
			}
		} else {
			for {
				if id, err = newRunID(); err != nil {
					return err
				}
				won, err := claimRun(tx, runRow{ID: id, ParentID: req.RunID, CreatedAt: time.Now().UTC()})
				if err != nil {
					return err
				}
				if won {
					break
				}
			}
		}

		for _, table := range []string{"agent_run_messages", "agent_run_events"} {
			if err := tx.Exec(
				fmt.Sprintf("INSERT INTO %s (run_id, body) SELECT ?, body FROM %s WHERE run_id = ? ORDER BY seq", table, table),
				id, req.RunID,
			).Error; err != nil {
				return err
			}
		}
		resp = agent.ForkRunResponse{RunID: id, Found: true, Created: true}
		return nil
	})
	return resp, err
}
