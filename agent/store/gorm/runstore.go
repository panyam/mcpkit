package gormstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
	ForkPoint int       `gorm:"not null;default:0"`
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

// AppendMessages implements agent.RunStore, stamping zero Timestamps
// per the agent.AppendMessagesRequest rule before encoding, so the
// stamp lives inside the stored JSON body and forks copy it for free.
func (s *RunStore) AppendMessages(ctx context.Context, req agent.AppendMessagesRequest) (agent.AppendMessagesResponse, error) {
	msgs := slices.Clone(req.Messages)
	now := time.Now().UTC()
	for i := range msgs {
		if msgs[i].Timestamp.IsZero() {
			msgs[i].Timestamp = now
		}
	}
	var found bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		found, err = appendRows(tx, req.RunID, msgs, func(tx *gorm.DB, runID string, bodies []string) error {
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
			ForkPoint: row.ForkPoint,
			CreatedAt: row.CreatedAt,
			Messages:  messages,
			Events:    events,
		},
		Found: true,
	}, nil
}

// ListRuns implements agent.RunStore, newest-first, with the message
// count computed by a correlated subquery so one query returns the whole
// page. The cursor is a decimal offset (keyset paging is overkill for a
// session picker; a run set is small).
func (s *RunStore) ListRuns(ctx context.Context, req agent.ListRunsRequest) (agent.ListRunsResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = agent.DefaultListRunsLimit
	}
	offset := 0
	if req.Cursor != "" {
		if n, err := strconv.Atoi(req.Cursor); err == nil && n > 0 {
			offset = n
		}
	}

	type row struct {
		ID           string
		ParentID     string
		ForkPoint    int
		CreatedAt    time.Time
		MessageCount int
	}
	var rows []row
	// fetch limit+1 to know whether a next page exists
	err := s.db.WithContext(ctx).
		Table("agent_runs AS r").
		Select("r.id, r.parent_id, r.fork_point, r.created_at, "+
			"(SELECT COUNT(*) FROM agent_run_messages m WHERE m.run_id = r.id) AS message_count").
		Order("r.created_at DESC, r.id ASC").
		Limit(limit + 1).Offset(offset).
		Scan(&rows).Error
	if err != nil {
		return agent.ListRunsResponse{}, err
	}

	var next string
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.Itoa(offset + limit)
	}
	infos := make([]agent.RunInfo, len(rows))
	for i, r := range rows {
		infos[i] = agent.RunInfo{
			ID: r.ID, ParentID: r.ParentID, ForkPoint: r.ForkPoint,
			CreatedAt: r.CreatedAt, MessageCount: r.MessageCount,
		}
	}
	return agent.ListRunsResponse{Runs: infos, NextCursor: next}, nil
}

// ForkRun implements agent.RunStore. The whole fork — source check,
// cut-point resolution, new run claim, and the log copies — runs in one
// transaction, so the fork is an exact cut of the source at commit
// time. See agent.ForkRunRequest for the AtMessage and event-log
// semantics.
//
// The transaction is for crash atomicity, NOT locking: nothing here
// takes row locks on the source (no FOR UPDATE; INSERT ... SELECT's
// read side is AccessShare), so appends to the source never wait on a
// fork. Races don't need the transaction either — a racing append gets
// a seq above every existing row, and the seq-ordered LIMIT copies a
// deterministic prefix, so it lands cleanly after the cut. What the
// transaction buys is the RunStore all-or-nothing contract: a crash
// mid-fork rolls back to nothing (no claim row over a partial copy for
// a retry to trust), and two replicas retrying the same NewRunID
// resolve to one complete fork plus one clean Created=false. The
// lock-free alternative (copy first, claim last, DELETE stale rows on
// retry) can satisfy the crash case but costs an extra statement,
// makes statement ORDER a correctness invariant, and leaves concurrent
// same-ID retries able to interleave each other's half-written copies
// — the transaction closes all three for free, so we use the
// database's native atomicity primitive, mirroring the Redis backend's
// choice of its native primitive (the atomic Lua script).
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

		var srcLen int64
		if err := tx.Table("agent_run_messages").Where("run_id = ?", req.RunID).Count(&srcLen).Error; err != nil {
			return err
		}
		n := int(srcLen)
		if req.AtMessage > 0 && req.AtMessage < n {
			n = req.AtMessage
		}

		id := req.NewRunID
		if id != "" {
			won, err := claimRun(tx, runRow{ID: id, ParentID: req.RunID, ForkPoint: n, CreatedAt: time.Now().UTC()})
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
				won, err := claimRun(tx, runRow{ID: id, ParentID: req.RunID, ForkPoint: n, CreatedAt: time.Now().UTC()})
				if err != nil {
					return err
				}
				if won {
					break
				}
			}
		}

		if n > 0 {
			if err := tx.Exec(
				"INSERT INTO agent_run_messages (run_id, body) SELECT ?, body FROM agent_run_messages WHERE run_id = ? ORDER BY seq LIMIT ?",
				id, req.RunID, n,
			).Error; err != nil {
				return err
			}
		}
		if n == int(srcLen) {
			if err := tx.Exec(
				"INSERT INTO agent_run_events (run_id, body) SELECT ?, body FROM agent_run_events WHERE run_id = ? ORDER BY seq",
				id, req.RunID,
			).Error; err != nil {
				return err
			}
		}
		resp = agent.ForkRunResponse{RunID: id, Found: true, Created: true, ForkPoint: n}
		return nil
	})
	return resp, err
}
