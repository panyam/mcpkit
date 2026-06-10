package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// eventBufferRow is the GORM model for one buffered event. One row per
// (source_name, sequence_no) — sequence_no is database-assigned (auto-
// increment) so cursor strings stay monotone within a source without
// per-process counters. Payload carries the full events.Event as JSON
// so the wire shape round-trips bit-for-bit (Meta, Cursor, Data, etc.).
//
// Eviction is via expires_at (set at Append time to NOW() + BufferTTL).
// The background sweeper (event_buffer_eviction.go) calls Truncate to
// drop expired rows. Each replica runs its own sweeper; DELETE is
// idempotent, races are benign.
type eventBufferRow struct {
	SourceName string `gorm:"primaryKey;not null"`
	SequenceNo int64  `gorm:"primaryKey;autoIncrement"`
	EventID    string `gorm:"not null"`
	Cursor     string `gorm:"not null;index:idx_event_buffer_source_cursor"`
	Payload    []byte `gorm:"type:bytea;not null"`
	Timestamp  string `gorm:"not null"`
	ExpiresAt  time.Time `gorm:"not null;index:idx_event_buffer_expires"`
}

// TableName pins the table to a stable name independent of GORM's
// pluralization rules; operators reading psql expect `event_buffer`.
func (eventBufferRow) TableName() string { return "event_buffer" }

// eventBufferStore is the GORM-backed events.EventBufferStore impl.
// Concurrency safety comes from the database (Postgres autoincrement
// is atomic; SQLite serializes the whole transaction). Per-source
// partitioning is enforced by the (source_name, sequence_no) primary
// key — no row from source A can collide with source B.
type eventBufferStore struct {
	db  *gorm.DB
	ttl time.Duration
}

// NewEventBufferStore returns a GORM-backed EventBufferStore. The
// supplied *gorm.DB must point at a writable connection; AutoMigrate
// runs on construction unless WithoutAutoMigrate is passed. The
// returned store carries the configured BufferTTL — every Append
// stamps expires_at = NOW() + BufferTTL.
//
// Caller owns the connection lifecycle. The background eviction
// sweeper (events.NewEventBufferEvictionSweeper) is a separate
// concern — caller decides whether to run it (typical: yes, one
// per replica).
func NewEventBufferStore(db *gorm.DB, opts ...Option) (events.EventBufferStore, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if !cfg.skipAutoMigrate {
		if err := db.AutoMigrate(&eventBufferRow{}); err != nil {
			return nil, err
		}
	}
	return &eventBufferStore{db: db, ttl: cfg.bufferTTL}, nil
}

// Append inserts the event with expires_at = NOW() + ttl. The
// caller's Event.Cursor is stored verbatim — the YieldingSource's
// atomic counter is the authoritative monotone-per-source source of
// cursor values; the DB just persists what the caller produced. The
// sequence_no column is autoincrement-assigned by the DB and serves
// as a stable PK + insertion-order tiebreaker; it's not visible on
// the wire.
//
// Required Event fields: EventID, Cursor (non-nil), Timestamp.
func (s *eventBufferStore) Append(ctx context.Context, req events.AppendEventRequest) (events.AppendEventResponse, error) {
	payload, err := json.Marshal(req.Event)
	if err != nil {
		return events.AppendEventResponse{}, err
	}
	cursor := ""
	if req.Event.Cursor != nil {
		cursor = *req.Event.Cursor
	}
	row := eventBufferRow{
		SourceName: req.SourceName,
		EventID:    req.Event.EventID,
		Cursor:     cursor,
		Payload:    payload,
		Timestamp:  req.Event.Timestamp,
		ExpiresAt:  time.Now().Add(s.ttl),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return events.AppendEventResponse{}, err
	}
	return events.AppendEventResponse{}, nil
}

// Poll returns events whose cursor is strictly greater than
// req.Cursor (numeric comparison), ordered by sequence_no ASC, up to
// Limit. Truncated=true when req.Cursor is older than the oldest
// surviving cursor for the source — the slice the client wanted was
// evicted.
//
// Cursors are compared via numeric parse to match the in-memory
// impl's contract (YieldingSource cursors are monotone int64 strings;
// lex compare would mis-rank "100" vs "9").
func (s *eventBufferStore) Poll(ctx context.Context, req events.PollEventsRequest) (events.PollEventsResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 1
	}
	c, _ := strconv.ParseInt(req.Cursor, 10, 64)

	// Truncated check: requested cursor < oldest surviving cursor for
	// this source. We use MIN over the cursor column (numerically) —
	// not sequence_no — so the floor reflects what's wire-visible.
	var oldest sql_NullInt64
	if c > 0 {
		if err := s.db.WithContext(ctx).
			Model(&eventBufferRow{}).
			Where("source_name = ?", req.SourceName).
			Select("MIN(CAST(cursor AS BIGINT)) as v").
			Scan(&oldest).Error; err != nil {
			// SQLite doesn't have BIGINT; fall back to INTEGER.
			if err := s.db.WithContext(ctx).
				Model(&eventBufferRow{}).
				Where("source_name = ?", req.SourceName).
				Select("MIN(CAST(cursor AS INTEGER)) as v").
				Scan(&oldest).Error; err != nil {
				return events.PollEventsResponse{}, err
			}
		}
	}

	var rows []eventBufferRow
	// CAST in the WHERE so the comparison is numeric.
	if err := s.db.WithContext(ctx).
		Where("source_name = ? AND CAST(cursor AS INTEGER) > ?", req.SourceName, c).
		Order("sequence_no ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return events.PollEventsResponse{}, err
	}

	out := events.PollEventsResponse{NextCursor: req.Cursor}
	for _, r := range rows {
		var ev events.Event
		if err := json.Unmarshal(r.Payload, &ev); err != nil {
			return events.PollEventsResponse{}, err
		}
		out.Events = append(out.Events, ev)
		out.NextCursor = r.Cursor
	}

	// No events matched but the source has some — NextCursor = Latest.
	if len(out.Events) == 0 {
		latest, err := s.Latest(ctx, events.LatestCursorRequest{SourceName: req.SourceName})
		if err != nil {
			return events.PollEventsResponse{}, err
		}
		if latest.Cursor != "" {
			out.NextCursor = latest.Cursor
		}
	}

	// Truncated = client asked for a cursor older than the oldest
	// surviving cursor.
	if c > 0 && oldest.Valid && c < oldest.V {
		out.Truncated = true
	}
	return out, nil
}

// Latest returns the source's most recent cursor. Empty when source
// has no rows.
func (s *eventBufferStore) Latest(ctx context.Context, req events.LatestCursorRequest) (events.LatestCursorResponse, error) {
	var row eventBufferRow
	err := s.db.WithContext(ctx).
		Where("source_name = ?", req.SourceName).
		Order("sequence_no DESC").
		Limit(1).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return events.LatestCursorResponse{}, nil
	}
	if err != nil {
		return events.LatestCursorResponse{}, err
	}
	return events.LatestCursorResponse{Cursor: row.Cursor}, nil
}

// Recent returns the most recent N events for the source, oldest-first
// within the returned slice.
func (s *eventBufferStore) Recent(ctx context.Context, req events.RecentEventsRequest) (events.RecentEventsResponse, error) {
	if req.N <= 0 {
		return events.RecentEventsResponse{}, nil
	}
	var rows []eventBufferRow
	if err := s.db.WithContext(ctx).
		Where("source_name = ?", req.SourceName).
		Order("sequence_no DESC").
		Limit(req.N).
		Find(&rows).Error; err != nil {
		return events.RecentEventsResponse{}, err
	}
	// rows are newest-first from the DB; flip to oldest-first.
	out := make([]events.Event, len(rows))
	for i, r := range rows {
		var ev events.Event
		if err := json.Unmarshal(r.Payload, &ev); err != nil {
			return events.RecentEventsResponse{}, err
		}
		out[len(rows)-1-i] = ev
	}
	return events.RecentEventsResponse{Events: out}, nil
}

// Truncate drops events whose cursor is <= BeforeCursor (numerically).
// BeforeCursor=="" drops the whole source. Numeric comparison via
// CAST so lex ordering doesn't lie ("100" < "9").
func (s *eventBufferStore) Truncate(ctx context.Context, req events.TruncateEventsRequest) (events.TruncateEventsResponse, error) {
	q := s.db.WithContext(ctx).Where("source_name = ?", req.SourceName)
	if req.BeforeCursor != "" {
		c, err := strconv.ParseInt(req.BeforeCursor, 10, 64)
		if err != nil {
			return events.TruncateEventsResponse{}, err
		}
		q = q.Where("CAST(cursor AS INTEGER) <= ?", c)
	}
	result := q.Delete(&eventBufferRow{})
	if result.Error != nil {
		return events.TruncateEventsResponse{}, result.Error
	}
	return events.TruncateEventsResponse{Removed: int(result.RowsAffected)}, nil
}

// sql_NullInt64 is a local wrapper to avoid importing database/sql
// just for a NullInt64. GORM's Scan accepts struct fields keyed by
// SELECT alias (`v` in our query).
type sql_NullInt64 struct {
	V     int64
	Valid bool
}

// Scan satisfies sql.Scanner so GORM can fill it from a nullable
// aggregate result (MIN() of an empty set returns NULL).
func (n *sql_NullInt64) Scan(value any) error {
	if value == nil {
		n.Valid = false
		return nil
	}
	switch v := value.(type) {
	case int64:
		n.V = v
	case int:
		n.V = int64(v)
	default:
		return errors.New("sql_NullInt64: unsupported scan type")
	}
	n.Valid = true
	return nil
}
